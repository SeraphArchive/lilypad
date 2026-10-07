package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"

	"lilypad/internal/deltacomm"
	"lilypad/internal/master"
)

// cardConverter is the CardConvert lookup used when a rolled card is already owned.
type cardConverter interface {
	CardConvert(cardID int64) (label string, itemID int64, ok bool)
}

// Validate the entire batch before any payment or result is persisted. A
// duplicate without its conversion definition must remain available to retry.
func validateLotteryRewards(md cardConverter, owned map[int64]bool, rewards []master.Reward) error {
	seen := make(map[int64]bool, len(owned))
	for id := range owned {
		seen[id] = true
	}
	for _, reward := range rewards {
		if reward.RewardID <= 0 {
			return errors.New("invalid lottery reward")
		}
		switch reward.Category {
		case master.RewardCategoryCard:
			if seen[reward.RewardID] {
				if md == nil {
					return errors.New("missing card conversion")
				}
				if _, id, ok := md.CardConvert(reward.RewardID); !ok || id <= 0 {
					return errors.New("missing card conversion")
				}
			}
			seen[reward.RewardID] = true
		case rewardCategoryItem, 10:
		default:
			return errors.New("unsupported lottery reward category")
		}
	}
	return nil
}

func grantLotteryRewards(md cardConverter, ownedCards map[int64]bool, lotteryID int64, rewards []master.Reward, now int64) (cards []deltacomm.Row, results []deltacomm.Row, itemDelta, currencyDelta map[int64]int64) {
	itemDelta = map[int64]int64{}
	currencyDelta = map[int64]int64{}
	seen := map[int64]bool{}
	for id := range ownedCards {
		seen[id] = true
	}
	for i, rw := range rewards {
		dup, label := false, ""
		switch rw.Category {
		case master.RewardCategoryCard:
			if seen[rw.RewardID] {
				dup = true
				if md != nil {
					if lbl, iid, ok := md.CardConvert(rw.RewardID); ok {
						label = lbl
						itemDelta[iid] += master.CardDuplicatePieceNum
					}
				}
			} else {
				seen[rw.RewardID] = true
				cards = append(cards, newCardRow(rw.RewardID, now))
			}
		case rewardCategoryItem:
			if rw.RewardID != 0 {
				itemDelta[rw.RewardID]++
			}
		case 10:
			if rw.RewardID != 0 {
				currencyDelta[rw.RewardID]++
			}
		}
		results = append(results, deltacomm.Row{
			"_index":                       i,
			"_masterLotteryId":             lotteryID,
			"_masterLotteryRewardId":       rw.RewardID,
			"_masterLotteryRewardCategory": rw.Category,
			"_isDuplicated":                dup,
			"_convertedItemLabel":          label,
		})
	}
	return cards, results, itemDelta, currencyDelta
}

func newCardRow(cardID, now int64) deltacomm.Row {
	return deltacomm.Row{
		"_masterCardId":                   cardID,
		"_level":                          0,
		"_exp":                            0,
		"_limitBreakLevel":                0,
		"_overrideLimitBreakLevel":        0,
		"_overrideLimitBreakLevelEnabled": false,
		"_isResonanceShift":               false,
		"_daphneUsedNum":                  0,
		"_joinedAt":                       now,
		"_abilityTreeParts":               []any{},
		"_autoEquipmentParam":             []any{},
		"_autoEquipmentRingElement":       []any{},
		"_autoEquipmentBraceletElement":   []any{},
	}
}

// nextLotteryRow increments the per-lottery counters. Official traffic adds 1 to
// _drawCount/_dailyDrawCount per request and adds rewardCount to _issueRewardCount.
func nextLotteryRow(prev []deltacomm.Row, lotteryID int64, rewardCount int, now int64) (deltacomm.Row, error) {
	row := deltacomm.Row{
		"_masterLotteryId":  lotteryID,
		"_drawCount":        int64(1),
		"_dailyDrawCount":   int64(1),
		"_lastDrawAt":       now,
		"_issueRewardCount": int64(rewardCount),
		"_step":             lotteryStep(prev, lotteryID),
		"_point":            0,
	}
	if err := addRowReward(row, "_step", 1); err != nil {
		return nil, err
	}
	for _, r := range prev {
		if asInt(r["_masterLotteryId"]) == lotteryID {
			for field, amount := range map[string]int64{"_drawCount": 1, "_dailyDrawCount": 1, "_issueRewardCount": int64(rewardCount)} {
				row[field] = r[field]
				if err := addRowReward(row, field, amount); err != nil {
					return nil, err
				}
			}
			row["_point"] = asInt(r["_point"])
			break
		}
	}
	return row, nil
}

func ownedCardIDs(rows []deltacomm.Row) map[int64]bool {
	out := map[int64]bool{}
	for _, r := range rows {
		if id := asInt(r["_masterCardId"]); id != 0 {
			out[id] = true
		}
	}
	return out
}

// applyItemConsume decreases _num (not _totalNum) and always returns the row,
// including a newly-created zero row — matching official leftover ticket rows.
func applyItemConsume(current []deltacomm.Row, id, cost int64) []deltacomm.Row {
	if id == 0 || cost <= 0 {
		return nil
	}
	items := indexByInt(current, "_id")
	row, existed := items[id]
	if !existed {
		row = deltacomm.Row{"_id": id, "_num": int64(0), "_reservedNum": 0,
			"_isAlreadyPossessed": false, "_totalNum": cost, "_isLocked": false}
	}
	n := asInt(row["_num"]) - cost
	if n < 0 {
		n = 0
	}
	row["_num"] = n
	return []deltacomm.Row{row}
}

func consumeTermStock(rows []deltacomm.Row, uids []string, cost int64) []deltacomm.Row {
	if len(uids) == 0 || cost <= 0 {
		return nil
	}
	byUID := map[string]deltacomm.Row{}
	for _, r := range rows {
		uid, _ := r["_uid"].(string)
		byUID[uid] = r
	}
	var out []deltacomm.Row
	// Validation has checked the whole selection. Spend the total once, in the
	// client's selection order, retaining any surplus in the last instance.
	for _, uid := range uids {
		if cost == 0 {
			break
		}
		r := byUID[uid]
		value := asInt(r["_value"])
		debit := cost
		if value < debit {
			debit = value
		}
		if debit == 0 {
			continue
		}
		r["_value"] = value - debit
		cost -= debit
		out = append(out, r)
	}
	return out
}

const rewardCategoryTermStock = 12

func newTermStockUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newTermStockRow(itemID, value, now, validForDays int64) deltacomm.Row {
	return deltacomm.Row{
		"_uid":             newTermStockUID(),
		"_termStockItemId": itemID,
		"_getTimestamp":    now,
		"_validTimestamp":  now + validForDays*86400,
		"_value":           value,
	}
}
