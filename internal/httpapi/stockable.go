package httpapi

import (
	"errors"
	"math"
	"net/http"
	"time"

	"lilypad/internal/deltacomm"
	"lilypad/internal/master"
	"lilypad/internal/store"
)

// stockableReceiveRequest is POST /api/stockable_regular_reward/receive.
type stockableReceiveRequest struct {
	Hdr      string `json:"hdr"`
	Category int64  `json:"category"`
}

// Weekly stockable-regular-reward terms reset Monday 04:00 JST
// (resetTermType 1). Both official samples land on that boundary.
const weeklyResetHour = 4

// weeklyTermIndex returns the count of Monday-04:00-JST term boundaries
// between from and to (negative clamps to 0).
func weeklyTermIndex(t int64) int64 {
	tm := time.Unix(t, 0).In(time.FixedZone("JST", jstOffsetSec))
	// Days since an arbitrary Monday epoch, then adjust for the 04:00 cut.
	day := time.Date(tm.Year(), tm.Month(), tm.Day(), 0, 0, 0, 0, tm.Location())
	if tm.Hour() < weeklyResetHour {
		day = day.Add(-24 * time.Hour)
	}
	// 2024-01-01 was a Monday.
	monday := time.Date(2024, 1, 1, 0, 0, 0, 0, tm.Location())
	return int64(day.Sub(monday) / (7 * 24 * time.Hour))
}

// nextWeeklyReset is the first Monday 04:00 JST strictly after now.
func nextWeeklyReset(now int64) int64 {
	idx := weeklyTermIndex(now)
	monday := time.Date(2024, 1, 1, weeklyResetHour, 0, 0, 0, time.FixedZone("JST", jstOffsetSec))
	return monday.Add(time.Duration(idx+1) * 7 * 24 * time.Hour).Unix()
}

// handleStockableRegularRewardReceive pays out the periodic stockable rewards
// for a category (officially: the weekly 異時層 ticket on the StoryHardMode top
// screen). The grant count is the number of weekly term boundaries elapsed
// since the last receive, capped at maxRewardNumPerTerm — this reproduces both
// official samples (8 elapsed weeks -> 8 tickets; a long-idle first claim ->
// the 10/term cap). _stockedNum/_overNum stay 0: no official sample ever
// stocked, so the stocking rule is not yet pinned.
func (s *Server) handleStockableRegularRewardReceive(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.playerIDChecked(w, r)
	if !ok {
		return
	}
	var req stockableReceiveRequest
	if err := s.decode(r, &req); err != nil {
		s.fail(w, r, 1, "bad stockable reward request")
		return
	}
	var defs []master.StockableRegularReward
	if s.master != nil {
		defs = s.master.StockableRegularRewards(req.Category)
	}
	now := time.Now().Unix()

	put := map[string]any{}
	var rewardRows []deltacomm.Row
	var itemRows, currencyRows, termRows []deltacomm.Row
	var gemDelta int64
	hashes, err := s.store.MutateWithGems(r.Context(), userID,
		[]string{"user_stockable_regular_reward", "user_item", "user_currency", "user_term_stock_item"},
		func(cur map[string][]deltacomm.Row) (deltacomm.Deltas, store.GemChange, error) {
			existing := indexByInt(cur["user_stockable_regular_reward"], "_masterStockableRegularRewardId")
			itemDelta := map[int64]int64{}
			currencyDelta := map[int64]int64{}
			for _, def := range defs {
				if !s.master.IsOpened(def.ReleaseLabel, now) {
					continue
				}
				prev := existing[def.ID]
				due := false
				var baseline int64
				if prev == nil {
					openAt, ok := s.master.ReleaseOpenAt(def.StockPeriodLabel)
					if !ok {
						continue
					}
					due = now >= openAt
					baseline = openAt
				} else {
					next := asInt(prev["_nextReceivableAt"])
					if next == -1 {
						continue // never receivable again
					}
					due = now >= next
					baseline = asInt(prev["_lastReceivedAt"])
				}
				if !due {
					if prev != nil {
						rewardRows = append(rewardRows, prev)
					}
					continue
				}
				granted := weeklyTermIndex(now) - weeklyTermIndex(baseline)
				if granted < 0 {
					granted = 0
				}
				if def.MaxRewardNumPerTerm > 0 && granted > def.MaxRewardNumPerTerm {
					granted = def.MaxRewardNumPerTerm
				}
				total := granted
				if prev != nil {
					var err error
					total, err = addReward(asInt(prev["_totalReceivedNum"]), granted)
					if err != nil {
						return deltacomm.Deltas{}, store.GemChange{}, err
					}
				}
				row := deltacomm.Row{
					"_masterStockableRegularRewardId": def.ID,
					"_receivedNum":                    granted,
					"_stockedNum":                     0,
					"_overNum":                        0,
					"_totalReceivedNum":               total,
					"_lastReceivedAt":                 now,
					"_nextReceivableAt":               nextWeeklyReset(now),
				}
				rewardRows = append(rewardRows, row)
				if granted > 0 && s.master != nil {
					gems, err := s.grantStockableReward(def, granted, now, itemDelta, currencyDelta, &termRows)
					if err != nil || gems > math.MaxInt64-gemDelta {
						return deltacomm.Deltas{}, store.GemChange{}, errors.New("invalid stockable reward")
					}
					gemDelta += gems
				}
			}
			d := deltacomm.Deltas{PutItems: map[string][]deltacomm.Row{}}
			if len(rewardRows) > 0 {
				d.PutItems["user_stockable_regular_reward"] = rewardRows
			}
			var err error
			itemRows, err = applyItemGrants(cur["user_item"], itemDelta)
			if err != nil {
				return deltacomm.Deltas{}, store.GemChange{}, err
			}
			if len(itemRows) > 0 {
				d.PutItems["user_item"] = itemRows
			}
			currencyRows, err = applyCurrencyGrants(cur["user_currency"], currencyDelta)
			if err != nil {
				return deltacomm.Deltas{}, store.GemChange{}, err
			}
			if len(currencyRows) > 0 {
				d.PutItems["user_currency"] = currencyRows
			}
			if len(termRows) > 0 {
				d.PutItems["user_term_stock_item"] = termRows
			}
			return d, store.GemChange{Add: gemDelta}, nil
		})
	if err != nil {
		s.log.Error("stockable_regular_reward/receive", "err", err)
		s.fail(w, r, 1, "stockable reward receive failed")
		return
	}
	if len(rewardRows) > 0 {
		put["user_stockable_regular_reward"] = rewardRows
	}
	if len(itemRows) > 0 {
		put["user_item"] = itemRows
	}
	if len(currencyRows) > 0 {
		put["user_currency"] = currencyRows
	}
	if len(termRows) > 0 {
		put["user_term_stock_item"] = termRows
	}
	s.respond(w, r, s.envelope(map[string]any{
		"tables": map[string]any{"putItems": put},
		"hashes": emptyMapAsArray(hashes),
	}))
}

// grantStockableReward resolves the reward content of a stockable reward and
// accumulates the grants for `granted` payouts. Returns the gem (HC) amount.
func (s *Server) grantStockableReward(def master.StockableRegularReward, granted, now int64, itemDelta, currencyDelta map[int64]int64, termRows *[]deltacomm.Row) (int64, error) {
	rw, ok := s.master.RewardByLabel(def.RewardLabel)
	if !ok {
		return 0, errors.New("stockable reward content unknown")
	}
	cat := asInt(rw["category"])
	perNum := asInt(rw["num"])
	if perNum <= 0 {
		return 0, errors.New("invalid stockable reward amount")
	}
	if granted < 0 || granted > math.MaxInt64/perNum {
		return 0, errors.New("stockable reward amount overflows")
	}
	total := perNum * granted
	label, _ := rw["masterLabel"].(string)
	switch cat {
	case rewardCategoryItem:
		if id, ok := s.master.ItemID(label); ok {
			if total > math.MaxInt64-itemDelta[id] {
				return 0, errors.New("stockable reward amount overflows")
			}
			itemDelta[id] += total
		} else {
			return 0, errors.New("unknown stockable item")
		}
	case 10: // currency
		if id, ok := s.master.CurrencyID(label); ok {
			if total > math.MaxInt64-currencyDelta[id] {
				return 0, errors.New("stockable reward amount overflows")
			}
			currencyDelta[id] += total
		} else {
			return 0, errors.New("unknown stockable currency")
		}
	case master.RewardCategoryHardCurrency:
		return total, nil
	case rewardCategoryTermStock:
		if total > 10000-int64(len(*termRows)) {
			return 0, errors.New("stockable term-stock batch exceeds limit")
		}
		if itemID, days, ok := s.master.TermStockItemByLabel(label); ok {
			for i := int64(0); i < total; i++ {
				*termRows = append(*termRows, newTermStockRow(itemID, 1, now, days))
			}
		} else {
			return 0, errors.New("unknown stockable term-stock item")
		}
	default:
		return 0, errors.New("unsupported stockable reward category")
	}
	return 0, nil
}
