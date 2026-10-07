package httpapi

import (
	"errors"
	"math/rand/v2"

	"lilypad/internal/deltacomm"
)

var (
	errInsufficientTickets   = errors.New("insufficient tickets")
	errInsufficientTermStock = errors.New("insufficient term-stock")
	errInsufficientItems     = errors.New("insufficient items")
)

// syncEnvelope is the ignoreSyncData=false shape: tables.putItems/replaceItems
// are always dicts (empty {} is ok; [] is not — the client treats a non-dict as
// failure) and hashes is {} or [].
func (s *Server) syncEnvelope(put, replace map[string]any, hashes map[string]string, extra map[string]any) map[string]any {
	if put == nil {
		put = map[string]any{}
	}
	if replace == nil {
		replace = map[string]any{}
	}
	merged := map[string]any{
		"tables": map[string]any{
			"putItems":     put,
			"replaceItems": replace,
		},
		"hashes": emptyMapAsArray(hashes),
	}
	for k, v := range extra {
		merged[k] = v
	}
	return s.envelope(merged)
}

func itemCount(rows []deltacomm.Row, id int64) int64 {
	if id == 0 {
		return 0
	}
	for _, r := range rows {
		if asInt(r["_id"]) == id {
			return asInt(r["_num"])
		}
	}
	return 0
}

func ensureTermStock(rows []deltacomm.Row, uids []string, cost int64) error {
	if cost == 0 {
		return nil
	}
	if cost < 0 || len(uids) == 0 {
		return errInsufficientTermStock
	}
	want := map[string]bool{}
	for _, u := range uids {
		if u == "" || want[u] {
			return errInsufficientTermStock
		}
		want[u] = true
	}
	seen := map[string]bool{}
	remaining := cost
	for _, r := range rows {
		uid, _ := r["_uid"].(string)
		if !want[uid] {
			continue
		}
		value := asInt(r["_value"])
		if value < 0 || seen[uid] {
			return errInsufficientTermStock
		}
		seen[uid] = true
		// Saturate at the required amount instead of overflowing a summed balance.
		if value >= remaining {
			remaining = 0
		} else {
			remaining -= value
		}
	}
	if len(seen) != len(want) || remaining != 0 {
		return errInsufficientTermStock
	}
	return nil
}

func lotteryStep(prev []deltacomm.Row, lotteryID int64) int64 {
	for _, r := range prev {
		if asInt(r["_masterLotteryId"]) == lotteryID {
			return asInt(r["_step"])
		}
	}
	return 0
}

func randN(n int) int {
	if n <= 0 {
		return 0
	}
	return rand.IntN(n)
}

func bumpEncore(rows []deltacomm.Row, shopID int64, rewardCount int, now int64) (deltacomm.Row, error) {
	var best deltacomm.Row
	bestDay := int64(-1)
	for _, r := range rows {
		if asInt(r["_masterLotteryShopId"]) != shopID {
			continue
		}
		if asInt(r["_drawDay"]) >= bestDay {
			bestDay = asInt(r["_drawDay"])
			best = r
		}
	}
	if best == nil {
		return nil, nil
	}
	if err := addRowReward(best, "_dailyDrawCount", 1); err != nil {
		return nil, err
	}
	if err := addRowReward(best, "_dailyTotalRewardCount", int64(rewardCount)); err != nil {
		return nil, err
	}
	_ = now
	return best, nil
}
