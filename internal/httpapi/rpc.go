package httpapi

import (
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"net/http"
	"time"

	"lilypad/internal/deltacomm"
	"lilypad/internal/gem"
	"lilypad/internal/master"
	"lilypad/internal/store"
)

// envelope returns the common server-authoritative response envelope with the
// given extra fields merged in.
func (s *Server) envelope(extra map[string]any) map[string]any {
	out := map[string]any{
		"code":          0,
		"serverCommand": []any{},
		"systemLock":    s.systemLocks,
		"lotteryShop":   s.lotteryShop,
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// handleMigrationPrepare returns just the envelope (no tables), matching the
// captured /api/user/migration/prepare response.
func (s *Server) handleMigrationPrepare(w http.ResponseWriter, r *http.Request) {
	s.respond(w, r, s.envelope(nil))
}

// handleRandomSetup seeds the player's user_random table with server-generated
// RNG state (one row per lottery type, matching the captured shape).
func (s *Server) handleRandomSetup(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.playerIDChecked(w, r)
	if !ok {
		return
	}
	rows := make([]deltacomm.Row, 0, 13)
	for lotteryType := 1; lotteryType <= 13; lotteryType++ {
		rows = append(rows, deltacomm.Row{
			"_lotteryType": lotteryType,
			"_seed":        rand.Uint32(),
			"_index":       10,
			"_x":           rand.Uint32(),
			"_y":           rand.Uint32(),
			"_z":           rand.Uint32(),
			"_w":           rand.Uint32(),
		})
	}
	d := deltacomm.Deltas{PutItems: map[string][]deltacomm.Row{"user_random": rows}}
	hashes, err := s.store.ApplyDeltas(r.Context(), userID, d, nil)
	if err != nil {
		s.log.Error("random/setup", "err", err)
		s.fail(w, r, 1, "random setup failed")
		return
	}
	s.respond(w, r, s.envelope(map[string]any{
		"tables": map[string]any{"putItems": map[string]any{"user_random": rows}},
		"hashes": hashes,
	}))
}

// lotteryDrawRequest is the subset of /api/lottery/draw we consume.
type lotteryDrawRequest struct {
	Hdr             string `json:"hdr"`
	MasterLotteryID int64  `json:"masterLotteryId"`
	RewardCount     int    `json:"rewardCount"`
	// ConsumeType is the player's chosen payment method (Gem/ChargeGem/Ticket/
	// TermStockItem*). It is NOT used to decide the gem charge: live banners send
	// consumeType:0 even on paid gem pulls, so GemCost is the authoritative spend
	// signal (see gemChargeCost). It only gates ticket-item consumption.
	ConsumeType       int64    `json:"consumeType"`
	GemCost           int64    `json:"gemCost"`
	TicketCost        int64    `json:"ticketCost"`
	TermStockItemUids []string `json:"termStockItemUids"`
	TermStockItemCost int64    `json:"termStockItemCost"`
}

// gemChargeCost decides how many gems a draw consumes. The client declares the
// gems it is spending in gemCost, and sends 0 when paying by ticket / term-stock
// / a free pull (confirmed in the captures: ticket pulls carry gemCost:0), so
// gemCost is the authoritative "this draw spends N gems" signal. The request's
// consumeType field is NOT reliable here — live banners send consumeType:0 even
// on a 300/3000-gem pull — so we deliberately do not gate on it, and a zero
// gemCost never charges (the player paid by ticket/term-stock/free, which the
// client tracks in its own tables).
//
// When the player IS spending gems (gemCost > 0) and validate is on, an
// authoritative gem cost from the local MasterLottery snapshot overrides the
// client's number (anti-cheat clamp). When the lottery is unknown to the
// snapshot (a live banner) or is not gem-paid there, the client's declared
// gemCost is used so paid draws on new banners still consume. With validate off,
// the client's gemCost is always used as-is.
func gemChargeCost(reqGemCost, masterGemCost int64, masterIsGemPaid, validate bool) int64 {
	if reqGemCost <= 0 {
		return 0
	}
	if validate && masterIsGemPaid && masterGemCost > 0 {
		return masterGemCost
	}
	return reqGemCost
}

// handleLotteryDraw performs a server-authoritative weighted gacha roll over the
// lottery's master rate/reward groups, grants new cards or converts duplicates
// to style pieces, increments lottery counters, and persists.
func (s *Server) handleLotteryDraw(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.playerIDChecked(w, r)
	if !ok {
		return
	}
	var req lotteryDrawRequest
	if err := s.decode(r, &req); err != nil {
		s.fail(w, r, 1, "bad lottery request")
		return
	}
	if req.RewardCount < 0 || req.RewardCount > master.MaxDrawCount || req.GemCost < 0 || req.TicketCost < 0 || req.TermStockItemCost < 0 {
		s.fail(w, r, 1, "invalid lottery amounts")
		return
	}
	now := time.Now().Unix()
	put := map[string]any{}
	var results []deltacomm.Row
	masterCT, masterCost, masterOK := s.master.LotteryConsume(req.MasterLotteryID, req.RewardCount)
	cost := gemChargeCost(req.GemCost, masterCost, masterOK && masterCT == master.LotteryConsumeChargeGem, s.cfg.GemValidateCost())

	hashes, err := s.store.MutateWithGems(r.Context(), userID,
		[]string{"user_card", "user_lottery", "user_item", "user_term_stock_item", "user_currency", "user_lottery_encore"},
		func(cur map[string][]deltacomm.Row) (deltacomm.Deltas, store.GemChange, error) {
			if req.ConsumeType == 2 && req.TicketCost > 0 && s.master != nil {
				if tid, _, ok := s.master.LotteryTicket(req.MasterLotteryID); ok {
					if itemCount(cur["user_item"], tid) < req.TicketCost {
						return deltacomm.Deltas{}, store.GemChange{}, errInsufficientTickets
					}
				} else {
					return deltacomm.Deltas{}, store.GemChange{}, errors.New("unknown lottery ticket")
				}
			}
			if err := ensureTermStock(cur["user_term_stock_item"], req.TermStockItemUids, req.TermStockItemCost); err != nil {
				return deltacomm.Deltas{}, store.GemChange{}, err
			}
			step := lotteryStep(cur["user_lottery"], req.MasterLotteryID)
			rewards, err := s.master.RollAtStep(req.MasterLotteryID, req.RewardCount, step, func(n int) int { return rand.IntN(n) })
			if err != nil {
				return deltacomm.Deltas{}, store.GemChange{}, err
			}
			if err := validateLotteryRewards(s.master, ownedCardIDs(cur["user_card"]), rewards); err != nil {
				return deltacomm.Deltas{}, store.GemChange{}, err
			}
			cards, res, pieceDelta, currencyDelta := grantLotteryRewards(s.master, ownedCardIDs(cur["user_card"]), req.MasterLotteryID, rewards, now)
			results = res
			if s.master != nil {
				if pid, ok := s.master.LotteryPointItemID(req.MasterLotteryID); ok && len(rewards) > 0 {
					pieceDelta[pid] += int64(len(rewards))
				}
			}
			var consumed []deltacomm.Row
			if req.ConsumeType == 2 && req.TicketCost > 0 && s.master != nil {
				if tid, _, ok := s.master.LotteryTicket(req.MasterLotteryID); ok {
					consumed = applyItemConsume(cur["user_item"], tid, req.TicketCost)
					cur["user_item"] = overlayItems(cur["user_item"], consumed)
				}
			}
			granted, err := applyItemGrants(cur["user_item"], pieceDelta)
			if err != nil {
				return deltacomm.Deltas{}, store.GemChange{}, err
			}
			itemRows := overlayItems(consumed, granted)
			termRows := consumeTermStock(cur["user_term_stock_item"], req.TermStockItemUids, req.TermStockItemCost)
			lotteryRow, err := nextLotteryRow(cur["user_lottery"], req.MasterLotteryID, len(rewards), now)
			if err != nil {
				return deltacomm.Deltas{}, store.GemChange{}, err
			}
			currencyRows, err := applyCurrencyGrants(cur["user_currency"], currencyDelta)
			if err != nil {
				return deltacomm.Deltas{}, store.GemChange{}, err
			}

			d := deltacomm.Deltas{
				PutItems: map[string][]deltacomm.Row{
					"user_lottery": {lotteryRow},
				},
				ReplaceItems: map[string][]deltacomm.Row{
					"user_lottery_result": results,
				},
			}
			put["user_lottery"] = d.PutItems["user_lottery"]
			if len(cards) > 0 {
				d.PutItems["user_card"] = cards
				put["user_card"] = cards
			}
			if len(itemRows) > 0 {
				d.PutItems["user_item"] = itemRows
				put["user_item"] = itemRows
			}
			if len(termRows) > 0 {
				d.PutItems["user_term_stock_item"] = termRows
				put["user_term_stock_item"] = termRows
			}
			if len(currencyRows) > 0 {
				d.PutItems["user_currency"] = currencyRows
				put["user_currency"] = currencyRows
			}
			if s.master != nil {
				if shopID, ok := s.master.LotteryShopID(req.MasterLotteryID); ok {
					if _, isEnc := s.master.EncoreForShop(shopID); isEnc {
						if row, err := bumpEncore(cur["user_lottery_encore"], shopID, len(rewards), now); err != nil {
							return deltacomm.Deltas{}, store.GemChange{}, err
						} else if row != nil {
							d.PutItems["user_lottery_encore"] = []deltacomm.Row{row}
							put["user_lottery_encore"] = d.PutItems["user_lottery_encore"]
						}
					}
				}
			}
			gch := store.GemChange{}
			if cost > 0 {
				gch.Consume = cost
			}
			return d, gch, nil
		})
	if err != nil {
		if errors.Is(err, gem.ErrInsufficient) {
			s.fail(w, r, 1, "insufficient gems")
			return
		}
		if errors.Is(err, errInsufficientTickets) {
			s.fail(w, r, 1, "insufficient tickets")
			return
		}
		if errors.Is(err, errInsufficientTermStock) {
			s.fail(w, r, 1, "insufficient term-stock")
			return
		}
		s.log.Error("lottery persist", "err", err)
		s.fail(w, r, 1, "lottery draw failed")
		return
	}
	s.respond(w, r, s.syncEnvelope(put, map[string]any{"user_lottery_result": results}, hashes, nil))
}

// --- mission/receive: mark the requested missions as received ---

type missionReceiveRequest struct {
	Hdr              string  `json:"hdr"`
	MasterMissionIDs []int64 `json:"masterMissionIds"`
}

func (s *Server) handleMissionReceive(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.playerIDChecked(w, r)
	if !ok {
		return
	}
	var req missionReceiveRequest
	if err := s.decode(r, &req); err != nil {
		s.fail(w, r, 1, "bad mission request")
		return
	}
	if len(req.MasterMissionIDs) > 1000 {
		s.fail(w, r, 1, "too many missions")
		return
	}
	var missionRows, currencyRows, itemRows []deltacomm.Row
	var profileRow deltacomm.Row
	var gemDelta int64
	hashes, err := s.store.MutateWithGems(r.Context(), userID,
		[]string{"user_mission", "user_currency", "user_item", "user_profile"},
		func(cur map[string][]deltacomm.Row) (deltacomm.Deltas, store.GemChange, error) {
			existing := indexByInt(cur["user_mission"], "_masterMissionId")
			currencyDelta := map[int64]int64{}
			itemDelta := map[int64]int64{}
			profileDelta := map[string]int64{}
			seen := map[int64]bool{}
			for _, id := range req.MasterMissionIDs {
				if seen[id] {
					continue
				}
				seen[id] = true
				if _, ok := s.master.MissionRewardGroup(id); !ok {
					return deltacomm.Deltas{}, store.GemChange{}, errors.New("unknown mission reward")
				}
				progress := int64(0)
				if prev, ok := existing[id]; ok {
					if asInt(prev["_state"]) == 3 {
						missionRows = append(missionRows, prev)
						continue
					}
					progress = asInt(prev["_progress"])
				}
				row := deltacomm.Row{
					"_masterMissionId": id,
					"_state":           3, // received
					"_progress":        progress,
					"_expireAt":        0,
					"_isDisplayed":     true,
				}
				if prev, ok := existing[id]; ok {
					// A claim changes its state, not its period, visibility or newer fields.
					row = prev
					row["_state"] = 3
				}
				missionRows = append(missionRows, row)
				if s.master != nil {
					if grp, ok := s.master.MissionRewardGroup(id); ok {
						if err := accumulateMissionRewards(s.master, grp, 1, len(cur["user_profile"]) > 0, true, currencyDelta, itemDelta, profileDelta, &gemDelta); err != nil {
							return deltacomm.Deltas{}, store.GemChange{}, err
						}
					}
				}
			}
			d := deltacomm.Deltas{PutItems: map[string][]deltacomm.Row{"user_mission": missionRows}}
			var err error
			currencyRows, err = applyCurrencyGrants(cur["user_currency"], currencyDelta)
			if err != nil {
				return deltacomm.Deltas{}, store.GemChange{}, err
			}
			itemRows, err = applyItemGrants(cur["user_item"], itemDelta)
			if err != nil {
				return deltacomm.Deltas{}, store.GemChange{}, err
			}
			if len(currencyRows) > 0 {
				d.PutItems["user_currency"] = currencyRows
			}
			if len(itemRows) > 0 {
				d.PutItems["user_item"] = itemRows
			}
			if len(profileDelta) > 0 && len(cur["user_profile"]) > 0 {
				profileRow = cur["user_profile"][0]
				for f, amt := range profileDelta {
					if err := addRowReward(profileRow, f, amt); err != nil {
						return deltacomm.Deltas{}, store.GemChange{}, err
					}
				}
				d.PutItems["user_profile"] = []deltacomm.Row{profileRow}
			}
			return d, store.GemChange{Add: gemDelta}, nil
		})
	if err != nil {
		s.log.Error("mission/receive", "err", err)
		s.fail(w, r, 1, "mission receive failed")
		return
	}
	put := map[string]any{"user_mission": missionRows}
	if len(currencyRows) > 0 {
		put["user_currency"] = currencyRows
	}
	if len(itemRows) > 0 {
		put["user_item"] = itemRows
	}
	if profileRow != nil {
		put["user_profile"] = []deltacomm.Row{profileRow}
	}
	s.respond(w, r, s.envelope(map[string]any{
		"tables":         map[string]any{"putItems": put},
		"hashes":         emptyMapAsArray(hashes),
		"pendMissionIds": []any{},
	}))
}

// --- gift/receive: move gift rewards into inventory and mark gifts received ---

type giftReceiveRequest struct {
	Hdr           string  `json:"hdr"`
	ClientGiftIDs []int64 `json:"clientGiftIds"`
	ServerGiftIDs []int64 `json:"serverGiftIds"`
}

const rewardCategoryItem = 1

func (s *Server) handleGiftReceive(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.playerIDChecked(w, r)
	if !ok {
		return
	}
	var req giftReceiveRequest
	if err := s.decode(r, &req); err != nil {
		s.fail(w, r, 1, "bad gift request")
		return
	}
	if len(req.ClientGiftIDs)+len(req.ServerGiftIDs) > 1000 {
		s.fail(w, r, 1, "too many gifts")
		return
	}
	now := time.Now().Unix()
	put := map[string]any{}
	var termRows []deltacomm.Row
	var gemDelta int64
	hashes, err := s.store.MutateWithGems(r.Context(), userID,
		[]string{"user_server_gift", "user_client_gift", "user_item", "user_term_stock_item"},
		func(cur map[string][]deltacomm.Row) (deltacomm.Deltas, store.GemChange, error) {
			serverGifts := indexByInt(cur["user_server_gift"], "_giftId")
			clientGifts := indexByInt(cur["user_client_gift"], "_giftId")

			var recvServer, recvClient []deltacomm.Row
			itemDelta := map[int64]int64{}
			// Only server gifts are granted server-authoritatively. Their rows are
			// self-describing (_rewardCategory + _itemId/_itemNum), and those grants
			// never reappear in user_reward_grant_log. Client gifts carry only
			// _masterRewardId: item/currency rewards are granted by the client itself
			// and pushed as table deltas, so crediting them here would double-grant.
			collect := func(g deltacomm.Row) error {
				switch asInt(g["_rewardCategory"]) {
				case rewardCategoryItem:
					id, num := asInt(g["_itemId"]), asInt(g["_itemNum"])
					if id <= 0 || num <= 0 || num > math.MaxInt64-itemDelta[id] {
						return errors.New("invalid gift item")
					}
					itemDelta[id] += num
				case rewardCategoryTermStock:
					itemID := asInt(g["_itemId"])
					num := asInt(g["_itemNum"])
					if itemID <= 0 || num <= 0 {
						return errors.New("invalid gift term-stock item")
					}
					days := int64(30)
					if s.master != nil {
						days = s.master.TermStockEffectiveDays(itemID)
					}
					termRows = append(termRows, newTermStockRow(itemID, num, now, days))
				case master.RewardCategoryHardCurrency:
					num := giftGemAmount(g)
					if num <= 0 || num > math.MaxInt64-gemDelta {
						return errors.New("invalid gift quartz amount")
					}
					gemDelta += num
				default:
					return errors.New("unsupported server gift reward category")
				}
				return nil
			}
			// Client gifts: the server still owns the two reward categories the
			// client cannot grant itself. Hard-currency rewards credit the payment
			// balance here at receive time (the client's isHc check triggers a
			// balance re-fetch right after this call); term-stock rewards need
			// server-minted uids, so the rows are created here with the gift's
			// registration time as the acquisition timestamp.
			collectClient := func(g deltacomm.Row) {
				if s.master == nil {
					return
				}
				cat, num, label, ok := s.master.RewardByID(asInt(g["_masterRewardId"]))
				if !ok {
					return
				}
				switch cat {
				case master.RewardCategoryHardCurrency:
					gemDelta += num
				case rewardCategoryTermStock:
					itemID, days, tok := s.master.TermStockItemByLabel(label)
					if !tok {
						return
					}
					if num <= 0 {
						num = 1
					}
					base := asInt(g["_registeredAt"])
					if base <= 0 {
						base = now
					}
					for i := int64(0); i < num; i++ {
						termRows = append(termRows, newTermStockRow(itemID, 1, base, days))
					}
				}
			}
			for _, id := range req.ServerGiftIDs {
				if g, ok := serverGifts[id]; ok && !asBool(g["_isReceived"]) {
					g["_isReceived"] = true
					g["_receivedAt"] = now
					recvServer = append(recvServer, g)
					if err := collect(g); err != nil {
						return deltacomm.Deltas{}, store.GemChange{}, err
					}
				}
			}
			for _, id := range req.ClientGiftIDs {
				if g, ok := clientGifts[id]; ok && !asBool(g["_isReceived"]) {
					category, num, label, known := s.master.RewardByID(asInt(g["_masterRewardId"]))
					if !known {
						return deltacomm.Deltas{}, store.GemChange{}, errors.New("gift reward missing from master data")
					}
					if category == rewardCategoryTermStock {
						if num <= 0 || num > 10000-int64(len(termRows)) {
							return deltacomm.Deltas{}, store.GemChange{}, errors.New("invalid gift term-stock amount")
						}
						if _, _, known := s.master.TermStockItemByLabel(label); !known {
							return deltacomm.Deltas{}, store.GemChange{}, errors.New("term-stock reward missing from master data")
						}
					}
					if category == master.RewardCategoryHardCurrency && (num <= 0 || num > math.MaxInt64-gemDelta) {
						return deltacomm.Deltas{}, store.GemChange{}, errors.New("invalid gift quartz amount")
					}
					g["_isReceived"] = true
					g["_receivedAt"] = now
					recvClient = append(recvClient, g)
					// Item/currency rewards are granted by the client and pushed
					// as deltas; only HC (gems) and term-stock are granted here.
					collectClient(g)
				}
			}
			changedItems, err := applyItemGrants(cur["user_item"], itemDelta)
			if err != nil {
				return deltacomm.Deltas{}, store.GemChange{}, err
			}

			d := deltacomm.Deltas{PutItems: map[string][]deltacomm.Row{}}
			if len(recvServer) > 0 {
				d.PutItems["user_server_gift"] = recvServer
				put["user_server_gift"] = recvServer
			}
			if len(recvClient) > 0 {
				d.PutItems["user_client_gift"] = recvClient
				put["user_client_gift"] = recvClient
			}
			if len(changedItems) > 0 {
				d.PutItems["user_item"] = changedItems
				put["user_item"] = changedItems
			}
			if len(termRows) > 0 {
				d.PutItems["user_term_stock_item"] = termRows
				put["user_term_stock_item"] = termRows
			}
			return d, store.GemChange{Add: gemDelta}, nil
		})
	if err != nil {
		s.log.Error("gift/receive", "err", err)
		s.fail(w, r, 1, "gift receive failed")
		return
	}
	s.respond(w, r, s.envelope(map[string]any{
		"tables":            map[string]any{"putItems": put},
		"hashes":            emptyMapAsArray(hashes),
		"pendClientGiftIds": []any{},
		"pendServerGiftIds": []any{},
	}))
}

func giftGemAmount(g deltacomm.Row) int64 {
	if n := asInt(g["_itemNum"]); n > 0 {
		return n
	}
	return asInt(g["_num"])
}

// --- daily/update: stamp profile, grant looping/start-dash login bonuses,
// reset weekday dailies + garden drops, increment login records ---

func (s *Server) handleDailyUpdate(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.playerIDChecked(w, r)
	if !ok {
		return
	}
	now := time.Now().Unix()
	put := map[string]any{}
	hashes, err := s.store.MutateUnderLock(r.Context(), userID,
		[]string{"user_profile", "user_life", "user_spirit", "user_login_bonus", "user_server_gift", "user_mission", "user_garden_drop_item", "user_record"},
		func(cur map[string][]deltacomm.Row) (deltacomm.Deltas, error) {
			d := deltacomm.Deltas{PutItems: map[string][]deltacomm.Row{}}
			var profile deltacomm.Row
			if len(cur["user_profile"]) > 0 {
				profile = cur["user_profile"][0]
			}
			if alreadyDailyUpdated(profile, now) {
				// Same JST day: still echo current stamina so the client has a
				// well-formed putItems, but do not grant again.
				if profile != nil {
					d.PutItems["user_profile"] = []deltacomm.Row{profile}
					put["user_profile"] = d.PutItems["user_profile"]
				}
				if rows := cur["user_life"]; len(rows) > 0 {
					d.PutItems["user_life"] = []deltacomm.Row{rows[0]}
					put["user_life"] = d.PutItems["user_life"]
				}
				if rows := cur["user_spirit"]; len(rows) > 0 {
					d.PutItems["user_spirit"] = []deltacomm.Row{rows[0]}
					put["user_spirit"] = d.PutItems["user_spirit"]
				}
				return d, nil
			}

			if profile != nil {
				profile["_lastDailyUpdatedAt"] = now
				for k := range profile {
					if dailyTimestampField(k) {
						profile[k] = now
					}
				}
				d.PutItems["user_profile"] = []deltacomm.Row{profile}
				put["user_profile"] = d.PutItems["user_profile"]
			} else {
				return deltacomm.Deltas{}, errors.New("save has no profile")
			}
			if rows := cur["user_life"]; len(rows) > 0 {
				d.PutItems["user_life"] = []deltacomm.Row{rows[0]}
				put["user_life"] = d.PutItems["user_life"]
			}
			if rows := cur["user_spirit"]; len(rows) > 0 {
				d.PutItems["user_spirit"] = []deltacomm.Row{rows[0]}
				put["user_spirit"] = d.PutItems["user_spirit"]
			}

			registeredAt := int64(0)
			if profile != nil {
				registeredAt = asInt(profile["_registeredAt"])
			}
			existingBonus := indexByInt(cur["user_login_bonus"], "_masterLoginId")
			var bonusRows []deltacomm.Row
			var giftRows []deltacomm.Row
			if s.master != nil {
				for _, bonus := range s.master.LoginBonuses() {
					prev := existingBonus[bonus.ID]
					started := prev != nil
					if !s.master.IsOpened(bonus.ReleaseLabel, now) {
						continue
					}
					if bonus.TimeLimitSec > 0 && registeredAt > 0 && now > registeredAt+bonus.TimeLimitSec && !started {
						continue
					}
					day, lap, ok := nextLoginBonusProgress(prev, bonus)
					if !ok {
						continue
					}
					bonusRows = append(bonusRows, deltacomm.Row{
						"_masterLoginId":   bonus.ID,
						"_lastReceivedLap": lap,
						"_lastReceivedDay": day,
						"_lastReceivedAt":  now,
					})
					gifts, err := loginBonusGifts(s.master, bonus, day, now)
					if err != nil {
						return deltacomm.Deltas{}, err
					}
					giftRows = append(giftRows, gifts...)
				}
			}
			if len(bonusRows) > 0 {
				d.PutItems["user_login_bonus"] = bonusRows
				put["user_login_bonus"] = bonusRows
			}
			if len(giftRows) > 0 {
				d.PutItems["user_server_gift"] = giftRows
				put["user_server_gift"] = giftRows
			}

			// Official new-player day 1 does not emit daily missions; they appear
			// from the second login day onward.
			hadPriorLogin := false
			for _, r := range cur["user_record"] {
				if asInt(r["_type"]) == recordLoginDays && asInt(r["_value"]) > 0 {
					hadPriorLogin = true
					break
				}
			}
			if s.master != nil && hadPriorLogin {
				expireAt := jstNextDailyReset(now)
				var missions []deltacomm.Row
				for _, id := range s.master.DailyMissionsForWeekday(int(jstWeekday(now))) {
					missions = append(missions, deltacomm.Row{
						"_masterMissionId": id,
						"_state":           0,
						"_progress":        0,
						"_expireAt":        expireAt,
						"_isDisplayed":     true,
					})
				}
				if len(missions) > 0 {
					d.PutItems["user_mission"] = missions
					put["user_mission"] = missions
				}
				var garden []deltacomm.Row
				for _, id := range s.master.OpenGardenDropIDs(now) {
					garden = append(garden, deltacomm.Row{"_masterGardenDropItemId": id, "_state": gardenDropReady})
				}
				if len(garden) > 0 {
					d.PutItems["user_garden_drop_item"] = garden
					put["user_garden_drop_item"] = garden
				}
			}

			records := []deltacomm.Row{
				incrementRecord(cur["user_record"], recordLoginDays),
				incrementRecord(cur["user_record"], recordLoginKindA),
				incrementRecord(cur["user_record"], recordLoginKindB),
			}
			d.PutItems["user_record"] = records
			put["user_record"] = records
			return d, nil
		})
	if err != nil {
		s.log.Error("daily/update", "err", err)
		s.fail(w, r, 1, "daily update failed")
		return
	}
	s.respond(w, r, s.envelope(map[string]any{
		"tables":                 map[string]any{"putItems": put},
		"hashes":                 emptyMapAsArray(hashes),
		"triggeredProductLabels": []any{},
	}))
}

var dailyTimestampFields = map[string]bool{
	"_lastDailyUpdatedAt":        true,
	"_lastReceivedLoginBonusAt":  true,
	"_lastGardenDropItemResetAt": true,
	"_lastDailyMissionResetAt":   true,
	"_lastDailyStaminaUpdatedAt": true,
	"_lastDailySpiritUpdatedAt":  true,
	"_lastDailyShopItemResetAt":  true,
}

func dailyTimestampField(k string) bool { return dailyTimestampFields[k] }

// --- row helpers ---

func indexByInt(rows []deltacomm.Row, key string) map[int64]deltacomm.Row {
	out := make(map[int64]deltacomm.Row, len(rows))
	for _, r := range rows {
		out[asInt(r[key])] = r
	}
	return out
}

func asInt(v any) int64 {
	switch t := v.(type) {
	case json.Number:
		n, _ := t.Int64()
		return n
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	default:
		return 0
	}
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// applyItemGrants merges item-quantity grants (itemID -> added num) into the
// current user_item rows, returning only the changed rows (additive _num/_totalNum).
func applyItemGrants(current []deltacomm.Row, grants map[int64]int64) ([]deltacomm.Row, error) {
	items := indexByInt(current, "_id")
	var changed []deltacomm.Row
	for id, num := range grants {
		if id == 0 || num == 0 {
			continue
		}
		row, existed := items[id]
		if !existed {
			row = deltacomm.Row{"_id": id, "_num": int64(0), "_reservedNum": 0,
				"_isAlreadyPossessed": false, "_totalNum": int64(0), "_isLocked": false}
		}
		if err := addRowReward(row, "_num", num); err != nil {
			return nil, err
		}
		if err := addRowReward(row, "_totalNum", num); err != nil {
			return nil, err
		}
		if existed {
			row["_isAlreadyPossessed"] = true
		}
		items[id] = row
		changed = append(changed, row)
	}
	return changed, nil
}

// applyCurrencyGrants merges currency grants (currencyID -> added num) into the
// current user_currency rows, returning the changed rows (absolute _num totals).
func applyCurrencyGrants(current []deltacomm.Row, grants map[int64]int64) ([]deltacomm.Row, error) {
	cur := indexByInt(current, "_id")
	var changed []deltacomm.Row
	for id, num := range grants {
		if num == 0 {
			continue
		}
		total, err := addReward(asInt(cur[id]["_num"]), num)
		if err != nil {
			return nil, err
		}
		changed = append(changed, deltacomm.Row{"_id": id, "_num": total})
	}
	return changed, nil
}
