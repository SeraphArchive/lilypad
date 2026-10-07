package httpapi

import (
	"math/rand/v2"
	"net/http"
	"time"

	"lilypad/internal/deltacomm"
)

// handleLotteryPrepare answers POST /api/lottery/prepare. The client sends
// ignoreSyncData=false, so the body MUST include tables+hashes. Encore shops in
// lotteryShop with no (or stale) user_lottery_encore row are minted here —
// DomainLotteryEncore.NeedsPrepare() aborts gacha open otherwise.
func (s *Server) handleLotteryPrepare(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.playerIDChecked(w, r)
	if !ok {
		return
	}
	now := time.Now().Unix()
	reset := jstDailyReset(now)
	put := map[string]any{}
	hashes, err := s.store.MutateUnderLock(r.Context(), userID,
		[]string{"user_lottery_encore"},
		func(cur map[string][]deltacomm.Row) (deltacomm.Deltas, error) {
			var minted []deltacomm.Row
			if s.master != nil {
				byShop := map[int64][]deltacomm.Row{}
				for _, row := range cur["user_lottery_encore"] {
					sid := asInt(row["_masterLotteryShopId"])
					byShop[sid] = append(byShop[sid], row)
				}
				for _, enc := range s.master.EncoreShops(s.lotteryShop, now) {
					rows := byShop[enc.ShopID]
					alreadyToday := false
					maxDay := int64(0)
					for _, row := range rows {
						if asInt(row["_drawDay"]) > maxDay {
							maxDay = asInt(row["_drawDay"])
						}
						if asInt(row["_preparedAt"]) >= reset {
							alreadyToday = true
						}
					}
					if alreadyToday {
						continue
					}
					nextDay, err := addReward(maxDay, 1)
					if err != nil {
						return deltacomm.Deltas{}, err
					}
					if nextDay < 1 {
						nextDay = 1
					}
					if enc.LimitDrawDay > 0 && nextDay > enc.LimitDrawDay {
						continue
					}
					sid, isGod := s.master.PickEncoreScenario(enc, func(n int) int { return rand.IntN(n) })
					minted = append(minted, deltacomm.Row{
						"_masterLotteryShopId":           enc.ShopID,
						"_drawDay":                       nextDay,
						"_masterLotteryEncoreScenarioId": sid,
						"_dailyDrawCount":                0,
						"_dailyTotalRewardCount":         0,
						"_isGod":                         isGod,
						"_createdAt":                     now,
						"_preparedAt":                    now,
					})
				}
			}
			if len(minted) == 0 {
				return deltacomm.Deltas{}, nil
			}
			put["user_lottery_encore"] = minted
			return deltacomm.Deltas{PutItems: map[string][]deltacomm.Row{"user_lottery_encore": minted}}, nil
		})
	if err != nil {
		s.log.Error("lottery/prepare", "err", err)
		s.fail(w, r, 1, "lottery prepare failed")
		return
	}
	s.respond(w, r, s.syncEnvelope(put, nil, hashes, nil))
}
