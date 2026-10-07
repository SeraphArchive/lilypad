package httpapi

import (
	"errors"
	"math"
	"net/http"
	"time"

	"lilypad/internal/deltacomm"
	"lilypad/internal/gem"
	"lilypad/internal/master"
	"lilypad/internal/store"
)

// handleUserProfile answers POST /api/user/profile (lookup other players by
// encryptedUserIds). A private server has no other-player index, so this is an
// envelope-correct empty snapshot — enough to keep the UI from E0.
func (s *Server) handleUserProfile(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.playerIDChecked(w, r); !ok {
		return
	}
	s.respond(w, r, s.syncEnvelope(nil, nil, nil, nil))
}

func (s *Server) handleLiveRanking(w http.ResponseWriter, r *http.Request) {
	s.handleRankingList(w, r)
}
func (s *Server) handleArcadeRankingList(w http.ResponseWriter, r *http.Request) {
	s.handleRankingList(w, r)
}
func (s *Server) handleWaveBattleRankingList(w http.ResponseWriter, r *http.Request) {
	s.handleRankingList(w, r)
}
func (s *Server) handleOctopusRankingList(w http.ResponseWriter, r *http.Request) {
	s.handleRankingList(w, r)
}

func (s *Server) handleRankingList(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.playerIDChecked(w, r); !ok {
		return
	}
	s.respond(w, r, s.syncEnvelope(nil, nil, nil, map[string]any{
		"maxRank": 0, "myRank": 0, "focusRank": 0,
	}))
}

func (s *Server) handleArcadeResultFetch(w http.ResponseWriter, r *http.Request) {
	s.handleRankingFetch(w, r)
}
func (s *Server) handleWaveBattleResultFetch(w http.ResponseWriter, r *http.Request) {
	s.handleRankingFetch(w, r)
}

func (s *Server) handleRankingFetch(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.playerIDChecked(w, r); !ok {
		return
	}
	s.respond(w, r, s.syncEnvelope(nil, nil, nil, nil))
}

type loopMissionReceiveRequest struct {
	Hdr             string              `json:"hdr"`
	ReceiveMissions []loopMissionAmount `json:"receiveMissions"`
}

type loopMissionAmount struct {
	MasterMissionID int64 `json:"masterMissionId"`
	Amount          int64 `json:"amount"`
	ReceivedCount   int64 `json:"receivedCount"`
	ClearCount      int64 `json:"clearCount"`
}

func (s *Server) handleLoopMissionReceive(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.playerIDChecked(w, r)
	if !ok {
		return
	}
	var req loopMissionReceiveRequest
	if err := s.decode(r, &req); err != nil {
		s.fail(w, r, 1, "bad loop-mission request")
		return
	}
	if len(req.ReceiveMissions) > 1000 {
		s.fail(w, r, 1, "too many missions")
		return
	}
	nowPut := map[string]any{}
	var gemDelta int64
	hashes, err := s.store.MutateWithGems(r.Context(), userID,
		[]string{"user_loop_mission", "user_currency", "user_item", "user_profile"},
		func(cur map[string][]deltacomm.Row) (deltacomm.Deltas, store.GemChange, error) {
			existing := indexByInt(cur["user_loop_mission"], "_masterMissionId")
			currencyDelta := map[int64]int64{}
			itemDelta := map[int64]int64{}
			profileDelta := map[string]int64{}
			var loopRows []deltacomm.Row
			positions := map[int64]int{}
			for _, m := range req.ReceiveMissions {
				prev := existing[m.MasterMissionID]
				already := int64(0)
				if prev != nil {
					already = asInt(prev["_receivedCount"])
				}
				if m.ReceivedCount < 0 || m.ClearCount < 0 || m.ReceivedCount > m.ClearCount || m.Amount < 0 {
					return deltacomm.Deltas{}, store.GemChange{}, errors.New("invalid mission counts")
				}
				grantTimes := m.ReceivedCount - already
				if grantTimes < 0 {
					grantTimes = 0
				}
				if grantTimes > 10000 {
					return deltacomm.Deltas{}, store.GemChange{}, errors.New("mission claim exceeds limit")
				}
				row := deltacomm.Row{
					"_masterMissionId": m.MasterMissionID,
					"_clearCount":      m.ClearCount,
					"_receivedCount":   m.ReceivedCount,
					"_isDisplayed":     true,
				}
				if prev != nil {
					// A delayed claim must not lower progress already saved by a
					// newer request or overwrite visibility and unknown fields.
					row = prev
					row["_clearCount"] = max(asInt(prev["_clearCount"]), m.ClearCount)
					row["_receivedCount"] = max(already, m.ReceivedCount)
				}
				if grantTimes > 0 {
					row["_isDisplayed"] = true
				}
				if i, seen := positions[m.MasterMissionID]; seen {
					loopRows[i] = row
				} else {
					positions[m.MasterMissionID] = len(loopRows)
					loopRows = append(loopRows, row)
				}
				existing[m.MasterMissionID] = row
				if grantTimes == 0 || s.master == nil {
					continue
				}
				grp, ok := s.master.MissionRewardGroup(m.MasterMissionID)
				if !ok {
					return deltacomm.Deltas{}, store.GemChange{}, errors.New("unknown mission reward")
				}
				if err := accumulateMissionRewards(s.master, grp, grantTimes, len(cur["user_profile"]) > 0, false, currencyDelta, itemDelta, profileDelta, &gemDelta); err != nil {
					return deltacomm.Deltas{}, store.GemChange{}, err
				}
			}
			d := deltacomm.Deltas{PutItems: map[string][]deltacomm.Row{}}
			if len(loopRows) > 0 {
				d.PutItems["user_loop_mission"] = loopRows
				nowPut["user_loop_mission"] = loopRows
			}
			if rows, err := applyCurrencyGrants(cur["user_currency"], currencyDelta); err != nil {
				return deltacomm.Deltas{}, store.GemChange{}, err
			} else if len(rows) > 0 {
				d.PutItems["user_currency"] = rows
				nowPut["user_currency"] = rows
			}
			if rows, err := applyItemGrants(cur["user_item"], itemDelta); err != nil {
				return deltacomm.Deltas{}, store.GemChange{}, err
			} else if len(rows) > 0 {
				d.PutItems["user_item"] = rows
				nowPut["user_item"] = rows
			}
			if len(profileDelta) > 0 && len(cur["user_profile"]) > 0 {
				profile := cur["user_profile"][0]
				for f, amt := range profileDelta {
					if err := addRowReward(profile, f, amt); err != nil {
						return deltacomm.Deltas{}, store.GemChange{}, err
					}
				}
				d.PutItems["user_profile"] = []deltacomm.Row{profile}
				nowPut["user_profile"] = d.PutItems["user_profile"]
			}
			return d, store.GemChange{Add: gemDelta}, nil
		})
	if err != nil {
		s.log.Error("mission/loop/receive", "err", err)
		s.fail(w, r, 1, "loop mission receive failed")
		return
	}
	s.respond(w, r, s.syncEnvelope(nowPut, nil, hashes, map[string]any{
		"pendMissions": []any{},
	}))
}

type itemLotteryDrawRequest struct {
	Hdr                 string `json:"hdr"`
	MasterItemLotteryID int64  `json:"masterItemLotteryId"`
	DrawCount           int    `json:"drawCount"`
	ConsumeItemCost     int64  `json:"consumeItemCost"`
	ConsumeItemLabel    string `json:"consumeItemLabel"`
}

func (s *Server) handleItemLotteryDraw(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.playerIDChecked(w, r)
	if !ok {
		return
	}
	if s.master == nil {
		s.fail(w, r, 1, "item lottery unavailable")
		return
	}
	var req itemLotteryDrawRequest
	if err := s.decode(r, &req); err != nil {
		s.fail(w, r, 1, "bad item-lottery request")
		return
	}
	def, ok := s.master.ItemLottery(req.MasterItemLotteryID)
	if !ok {
		s.fail(w, r, 1, "item lottery unavailable")
		return
	}
	n := req.DrawCount
	if n <= 0 {
		n = 1
	}
	if n > master.MaxDrawCount || req.ConsumeItemCost < 0 {
		s.fail(w, r, 1, "invalid draw count")
		return
	}
	if def.DrawLimitPerRequest > 0 && int64(n) > def.DrawLimitPerRequest {
		s.fail(w, r, 1, "draw count exceeds lottery limit")
		return
	}
	if def.ConsumeCount < 0 || def.ConsumeCount > math.MaxInt64/int64(n) {
		s.fail(w, r, 1, "invalid lottery cost definition")
		return
	}
	now := time.Now().Unix()
	put := map[string]any{}
	var results []deltacomm.Row
	var gemDelta int64
	var termRows []deltacomm.Row
	hashes, err := s.store.MutateWithGems(r.Context(), userID,
		[]string{"user_item_lottery", "user_item_lottery_reward", "user_item", "user_currency", "user_profile"},
		func(cur map[string][]deltacomm.Row) (deltacomm.Deltas, store.GemChange, error) {
			previous := indexByInt(cur["user_item_lottery"], "_masterItemLotteryId")[def.ID]
			if previous != nil {
				if asBool(previous["_isTerminated"]) {
					return deltacomm.Deltas{}, store.GemChange{}, errors.New("lottery terminated")
				}
				daily := int64(0)
				if asInt(previous["_lastDrawAt"]) >= jstDailyReset(now) {
					daily = asInt(previous["_dailyDrawCount"])
				}
				if def.DailyLimit > 0 && daily+int64(n) > def.DailyLimit || def.TotalLimit > 0 && asInt(previous["_totalDrawCount"])+int64(n) > def.TotalLimit {
					return deltacomm.Deltas{}, store.GemChange{}, errors.New("lottery draw limit reached")
				}
			} else if def.DailyLimit > 0 && int64(n) > def.DailyLimit || def.TotalLimit > 0 && int64(n) > def.TotalLimit {
				return deltacomm.Deltas{}, store.GemChange{}, errors.New("lottery draw limit reached")
			}
			cost := def.ConsumeCount * int64(n)
			label := def.ConsumeLabel
			dropCounts := map[int64]int64{}
			for _, r := range cur["user_item_lottery_reward"] {
				dropCounts[asInt(r["_masterItemLotteryRewardId"])] = asInt(r["_dropCount"])
			}
			roll, err := s.master.RollItemLotteryState(req.MasterItemLotteryID, n, dropCounts, asInt(previous["_nonPickupDrawCount"]), func(n int) int {
				return randIntN(n)
			})
			if err != nil {
				return deltacomm.Deltas{}, store.GemChange{}, err
			}
			picks := roll.Picks
			actualCount := len(picks)
			cost = def.ConsumeCount * int64(actualCount)
			if label != "" && cost > 0 {
				itemID, ok := s.master.ItemID(label)
				if !ok || itemCount(cur["user_item"], itemID) < cost {
					return deltacomm.Deltas{}, store.GemChange{}, errInsufficientItems
				}
			}
			dropCounts = roll.DropCounts
			if roll.NonPickupCount < 0 {
				return deltacomm.Deltas{}, store.GemChange{}, errors.New("item lottery pity counter overflows")
			}
			for _, count := range dropCounts {
				if count < 0 {
					return deltacomm.Deltas{}, store.GemChange{}, errors.New("item lottery drop counter overflows")
				}
			}
			itemDelta := map[int64]int64{}
			currencyDelta := map[int64]int64{}
			profileDelta := map[string]int64{}
			for i, p := range picks {
				var rewardList []any
				var completionList []any
				allRewards := append(append([]master.GroupReward{}, p.MasterReward...), p.CompleteRewards...)
				for j, rw := range allRewards {
					entry := deltacomm.Row{
						"_index": j, "_masterRewardId": rw.ID, "_grantedNum": rw.Num, "_overNum": 0,
					}
					if j < len(p.MasterReward) {
						rewardList = append(rewardList, entry)
					} else {
						entry["_index"] = j - len(p.MasterReward)
						completionList = append(completionList, entry)
					}
					switch rw.Category {
					case master.RewardCategoryHardCurrency:
						var err error
						gemDelta, err = addReward(gemDelta, rw.Num)
						if err != nil {
							return deltacomm.Deltas{}, store.GemChange{}, err
						}
					case rewardCategoryItem:
						if id, ok := s.master.ItemID(rw.MasterLabel); ok {
							if err := accumulateReward(itemDelta, id, rw.Num); err != nil {
								return deltacomm.Deltas{}, store.GemChange{}, err
							}
						} else {
							return deltacomm.Deltas{}, store.GemChange{}, errors.New("unknown item reward")
						}
					case 10:
						if id, ok := s.master.CurrencyID(rw.MasterLabel); ok {
							if err := accumulateReward(currencyDelta, id, rw.Num); err != nil {
								return deltacomm.Deltas{}, store.GemChange{}, err
							}
						} else {
							return deltacomm.Deltas{}, store.GemChange{}, errors.New("unknown currency reward")
						}
					case rewardCategoryTermStock:
						id, days, ok := s.master.TermStockItemByLabel(rw.MasterLabel)
						if !ok || rw.Num < 0 || rw.Num > 10000 {
							return deltacomm.Deltas{}, store.GemChange{}, errors.New("invalid term-stock reward")
						}
						if int64(len(termRows))+rw.Num > 10000 {
							return deltacomm.Deltas{}, store.GemChange{}, errors.New("term-stock batch exceeds limit")
						}
						for j := int64(0); j < rw.Num; j++ {
							termRows = append(termRows, newTermStockRow(id, 1, now, days))
						}
					case 13:
						if len(cur["user_profile"]) == 0 {
							return deltacomm.Deltas{}, store.GemChange{}, errors.New("missing item-lottery reward profile")
						}
						if err := accumulateReward(profileDelta, "_limitBreakPower", rw.Num); err != nil {
							return deltacomm.Deltas{}, store.GemChange{}, err
						}
					default:
						return deltacomm.Deltas{}, store.GemChange{}, errors.New("unsupported item-lottery reward category")
					}
				}
				if completionList == nil {
					completionList = []any{}
				}
				results = append(results, deltacomm.Row{
					"_index":                     i,
					"_masterItemLotteryId":       req.MasterItemLotteryID,
					"_masterItemLotteryRewardId": p.ID,
					"_rewardList":                rewardList,
					"_completeReward":            completionList,
				})
			}
			lotteryRow, err := nextItemLotteryRow(cur["user_item_lottery"], req.MasterItemLotteryID, actualCount, now)
			if err != nil {
				return deltacomm.Deltas{}, store.GemChange{}, err
			}
			lotteryRow["_nonPickupDrawCount"] = roll.NonPickupCount
			if err := addRowReward(lotteryRow, "_resetCount", roll.ResetCount); err != nil {
				return deltacomm.Deltas{}, store.GemChange{}, err
			}
			lotteryRow["_isTerminated"] = roll.Terminated
			var dropRows []deltacomm.Row
			existingDrops := indexByInt(cur["user_item_lottery_reward"], "_masterItemLotteryRewardId")
			for id, cnt := range dropCounts {
				row, ok := existingDrops[id]
				if !ok {
					row = deltacomm.Row{"_masterItemLotteryRewardId": id, "_dropCount": cnt}
				} else {
					row["_dropCount"] = cnt
				}
				dropRows = append(dropRows, row)
			}
			d := deltacomm.Deltas{
				PutItems:     map[string][]deltacomm.Row{"user_item_lottery": {lotteryRow}},
				ReplaceItems: map[string][]deltacomm.Row{"user_item_lottery_result": results},
			}
			put["user_item_lottery"] = d.PutItems["user_item_lottery"]
			var consumed []deltacomm.Row
			if label != "" && cost > 0 {
				if itemID, ok := s.master.ItemID(label); ok {
					consumed = applyItemConsume(cur["user_item"], itemID, cost)
					cur["user_item"] = overlayItems(cur["user_item"], consumed)
				}
			}
			granted, err := applyItemGrants(cur["user_item"], itemDelta)
			if err != nil {
				return deltacomm.Deltas{}, store.GemChange{}, err
			}
			if rows := overlayItems(consumed, granted); len(rows) > 0 {
				d.PutItems["user_item"] = rows
				put["user_item"] = rows
			}
			if rows, err := applyCurrencyGrants(cur["user_currency"], currencyDelta); err != nil {
				return deltacomm.Deltas{}, store.GemChange{}, err
			} else if len(rows) > 0 {
				d.PutItems["user_currency"] = rows
				put["user_currency"] = rows
			}
			if len(dropRows) > 0 {
				d.PutItems["user_item_lottery_reward"] = dropRows
				put["user_item_lottery_reward"] = dropRows
			}
			if len(termRows) > 0 {
				d.PutItems["user_term_stock_item"] = termRows
				put["user_term_stock_item"] = termRows
			}
			if len(profileDelta) > 0 && len(cur["user_profile"]) > 0 {
				profile := cur["user_profile"][0]
				for f, amt := range profileDelta {
					if err := addRowReward(profile, f, amt); err != nil {
						return deltacomm.Deltas{}, store.GemChange{}, err
					}
				}
				d.PutItems["user_profile"] = []deltacomm.Row{profile}
				put["user_profile"] = d.PutItems["user_profile"]
			}
			return d, store.GemChange{Add: gemDelta}, nil
		})
	if err != nil {
		if errors.Is(err, errInsufficientItems) {
			s.fail(w, r, 1, "insufficient items")
			return
		}
		if errors.Is(err, gem.ErrInsufficient) {
			s.fail(w, r, 1, "insufficient gems")
			return
		}
		s.log.Error("item/lottery/draw", "err", err)
		s.fail(w, r, 1, "item lottery draw failed")
		return
	}
	s.respond(w, r, s.syncEnvelope(put, map[string]any{"user_item_lottery_result": results}, hashes, nil))
}

func nextItemLotteryRow(prev []deltacomm.Row, lotteryID int64, n int, now int64) (deltacomm.Row, error) {
	row := deltacomm.Row{
		"_masterItemLotteryId": lotteryID,
		"_totalDrawCount":      int64(n),
		"_dailyDrawCount":      int64(n),
		"_lastDrawAt":          now,
		"_nonPickupDrawCount":  0,
		"_isTerminated":        false,
		"_resetCount":          0,
	}
	for _, r := range prev {
		if asInt(r["_masterItemLotteryId"]) == lotteryID {
			row["_totalDrawCount"] = r["_totalDrawCount"]
			if err := addRowReward(row, "_totalDrawCount", int64(n)); err != nil {
				return nil, err
			}
			if asInt(r["_lastDrawAt"]) >= jstDailyReset(now) {
				row["_dailyDrawCount"] = r["_dailyDrawCount"]
				if err := addRowReward(row, "_dailyDrawCount", int64(n)); err != nil {
					return nil, err
				}
			}
			row["_nonPickupDrawCount"] = asInt(r["_nonPickupDrawCount"])
			row["_resetCount"] = asInt(r["_resetCount"])
			row["_isTerminated"] = asBool(r["_isTerminated"])
			break
		}
	}
	return row, nil
}

func overlayItems(base, extra []deltacomm.Row) []deltacomm.Row {
	idx := indexByInt(base, "_id")
	for _, r := range extra {
		idx[asInt(r["_id"])] = r
	}
	out := make([]deltacomm.Row, 0, len(idx))
	seen := map[int64]bool{}
	for _, r := range extra {
		id := asInt(r["_id"])
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, idx[id])
	}
	for _, r := range base {
		id := asInt(r["_id"])
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, r)
	}
	return out
}

type lotteryExchangeRequest struct {
	Hdr                         string   `json:"hdr"`
	MasterSelectTicketLotteryID int64    `json:"masterSelectTicketLotteryId"`
	RewardCount                 int      `json:"rewardCount"`
	TermStockItemCost           int64    `json:"termStockItemCost"`
	TermStockItemUids           []string `json:"termStockItemUids"`
	SelectCardID                int64    `json:"selectCardId"`
	SelectRewardID              int64    `json:"selectRewardId"`
}

func (s *Server) handleLotteryCardExchange(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.playerIDChecked(w, r)
	if !ok {
		return
	}
	if s.master == nil {
		s.fail(w, r, 1, "lottery exchange unavailable")
		return
	}
	var req lotteryExchangeRequest
	if err := s.decode(r, &req); err != nil {
		s.fail(w, r, 1, "bad lottery exchange request")
		return
	}
	def, ok := s.master.SelectTicketLottery(req.MasterSelectTicketLotteryID)
	if !ok {
		s.fail(w, r, 1, "lottery exchange unavailable")
		return
	}
	rewardID := req.SelectRewardID
	cardID := req.SelectCardID
	if rewardID != 0 {
		if id, ok := s.master.SelectTicketRewardCard(rewardID); ok && (cardID == 0 || cardID == id) {
			cardID = id
		} else {
			s.fail(w, r, 1, "unknown exchange reward")
			return
		}
	} else if cardID != 0 {
		if id, ok := s.master.SelectTicketRewardByCard(cardID); ok {
			rewardID = id
		}
	}
	if cardID == 0 || rewardID == 0 {
		s.fail(w, r, 1, "unknown exchange reward")
		return
	}
	cost := req.TermStockItemCost
	if cost <= 0 {
		cost = def.TermStockItemCost
	}
	now := time.Now().Unix()
	put := map[string]any{}
	var results []deltacomm.Row
	hashes, err := s.store.MutateUnderLock(r.Context(), userID,
		[]string{"user_card", "user_item", "user_term_stock_item", "user_select_ticket_lottery"},
		func(cur map[string][]deltacomm.Row) (deltacomm.Deltas, error) {
			if def.LimitDrawCount > 0 {
				for _, r := range cur["user_select_ticket_lottery"] {
					if asInt(r["_masterSelectTicketLotteryId"]) == def.ID && asInt(r["_drawCount"]) >= def.LimitDrawCount {
						return deltacomm.Deltas{}, errInsufficientItems
					}
				}
			}
			if err := ensureTermStock(cur["user_term_stock_item"], req.TermStockItemUids, cost); err != nil {
				return deltacomm.Deltas{}, err
			}
			if err := validateLotteryRewards(s.master, ownedCardIDs(cur["user_card"]), []master.Reward{{Category: master.RewardCategoryCard, RewardID: cardID}}); err != nil {
				return deltacomm.Deltas{}, err
			}
			cards, res, itemDelta, _ := grantLotteryRewards(s.master, ownedCardIDs(cur["user_card"]), 0,
				[]master.Reward{{Category: master.RewardCategoryCard, RewardID: cardID}}, now)
			if len(res) > 0 {
				res[0]["_masterSelectTicketLotteryId"] = def.ID
				res[0]["_masterSelectTicketLotteryRewardId"] = rewardID
				delete(res[0], "_masterLotteryId")
				delete(res[0], "_masterLotteryRewardId")
				delete(res[0], "_masterLotteryRewardCategory")
			}
			results = res
			termRows := consumeTermStock(cur["user_term_stock_item"], req.TermStockItemUids, cost)
			sel, err := nextSelectTicketRow(cur["user_select_ticket_lottery"], def.ID, now)
			if err != nil {
				return deltacomm.Deltas{}, err
			}
			d := deltacomm.Deltas{
				PutItems:     map[string][]deltacomm.Row{"user_select_ticket_lottery": {sel}},
				ReplaceItems: map[string][]deltacomm.Row{"user_select_ticket_lottery_result": results},
			}
			put["user_select_ticket_lottery"] = d.PutItems["user_select_ticket_lottery"]
			if len(cards) > 0 {
				d.PutItems["user_card"] = cards
				put["user_card"] = cards
			}
			if rows, err := applyItemGrants(cur["user_item"], itemDelta); err != nil {
				return deltacomm.Deltas{}, err
			} else if len(rows) > 0 {
				d.PutItems["user_item"] = rows
				put["user_item"] = rows
			}
			if len(termRows) > 0 {
				d.PutItems["user_term_stock_item"] = termRows
				put["user_term_stock_item"] = termRows
			}
			return d, nil
		})
	if err != nil {
		if errors.Is(err, errInsufficientTermStock) || errors.Is(err, errInsufficientItems) {
			s.fail(w, r, 1, "insufficient exchange cost")
			return
		}
		s.log.Error("lottery/card/exchange", "err", err)
		s.fail(w, r, 1, "lottery exchange failed")
		return
	}
	s.respond(w, r, s.syncEnvelope(put, map[string]any{"user_select_ticket_lottery_result": results}, hashes, nil))
}

func nextSelectTicketRow(prev []deltacomm.Row, lotteryID, now int64) (deltacomm.Row, error) {
	row := deltacomm.Row{"_masterSelectTicketLotteryId": lotteryID, "_drawCount": int64(1), "_lastDrawAt": now}
	for _, r := range prev {
		if asInt(r["_masterSelectTicketLotteryId"]) == lotteryID {
			row["_drawCount"] = r["_drawCount"]
			if err := addRowReward(row, "_drawCount", 1); err != nil {
				return nil, err
			}
			break
		}
	}
	return row, nil
}

func randIntN(n int) int {
	if n <= 0 {
		return 0
	}
	return randN(n)
}
