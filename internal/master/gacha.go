package master

import (
	"fmt"
)

// Reward category constants observed in MasterLotteryReward.
const RewardCategoryCard = 9

// MaxDrawCount bounds allocation even for malformed/custom master definitions.
const MaxDrawCount = 10000

// Reward is a single granted reward from a gacha roll.
type Reward struct {
	Category int64
	RewardID int64
}

// IntnFunc returns a pseudo-random int in [0, n); injected so rolls are testable.
type IntnFunc func(n int) int

// LotteryInfo summarizes a lottery definition.
type LotteryInfo struct {
	ID          int64
	RateGroupID int64
	RewardCount int
}

// Lottery returns the lottery definition by id.
func (d *Data) Lottery(id int64) (LotteryInfo, bool) {
	row, ok := d.lotteryByID[id]
	if !ok {
		return LotteryInfo{}, false
	}
	rg, _ := num(row["masterLotteryRateGroupId"])
	rc, _ := num(row["rewardCount"])
	return LotteryInfo{ID: id, RateGroupID: rg, RewardCount: int(rc)}, true
}

// rollOne performs a single weighted draw: pick a rarity tier within the rate
// group by its rate weight, then a uniform reward from that tier's reward group.
func (d *Data) rollOne(rateGroupID int64, intn IntnFunc) (Reward, error) {
	rates := d.rateByGroup[rateGroupID]
	if len(rates) == 0 {
		return Reward{}, fmt.Errorf("master: no rates for group %d", rateGroupID)
	}
	total := int64(0)
	for _, r := range rates {
		w, _ := num(r["rate"])
		total += w
	}
	if total <= 0 {
		return Reward{}, fmt.Errorf("master: zero total rate for group %d", rateGroupID)
	}
	pick := int64(intn(int(total)))
	var chosen Row
	for _, r := range rates {
		w, _ := num(r["rate"])
		if pick < w {
			chosen = r
			break
		}
		pick -= w
	}
	if chosen == nil {
		chosen = rates[len(rates)-1]
	}
	rewardGroupID, _ := num(chosen["masterLotteryRewardGroupId"])
	rewards := d.rewardByGroup[rewardGroupID]
	if len(rewards) == 0 {
		return Reward{}, fmt.Errorf("master: no rewards for group %d", rewardGroupID)
	}
	rw := rewards[intn(len(rewards))]
	cat, _ := num(rw["rewardCategory"])
	rid, _ := num(rw["rewardId"])
	return Reward{Category: cat, RewardID: rid}, nil
}

// Roll draws count rewards for a lottery at step 0.
func (d *Data) Roll(lotteryID int64, count int, intn IntnFunc) ([]Reward, error) {
	return d.RollAtStep(lotteryID, count, 0, intn)
}

// LotteryShopID is MasterLottery.masterLotteryShopId.
func (d *Data) LotteryShopID(lotteryID int64) (int64, bool) {
	row, ok := d.lotteryByID[lotteryID]
	if !ok {
		return 0, false
	}
	id, ok := num(row["masterLotteryShopId"])
	return id, ok && id > 0
}

// rateGroupForStep returns the rate group used at the current pity/step counter.
// When step >= replaceRateGroupStep the banner switches to replaceMasterLotteryRateGroupId.
func (d *Data) rateGroupForStep(lotteryID, step int64) int64 {
	row, ok := d.lotteryByID[lotteryID]
	if !ok {
		return 0
	}
	rg, _ := num(row["masterLotteryRateGroupId"])
	replaceStep, _ := num(row["replaceRateGroupStep"])
	replaceRG, _ := num(row["replaceMasterLotteryRateGroupId"])
	if replaceStep > 0 && replaceRG > 0 && step >= replaceStep {
		return replaceRG
	}
	return rg
}

// RollAtStep draws count rewards using the rate group for the given _step.
func (d *Data) RollAtStep(lotteryID int64, count int, step int64, intn IntnFunc) ([]Reward, error) {
	info, ok := d.Lottery(lotteryID)
	if !ok {
		return nil, fmt.Errorf("master: unknown lottery %d", lotteryID)
	}
	if count <= 0 {
		count = info.RewardCount
	}
	if count > MaxDrawCount {
		return nil, fmt.Errorf("master: draw count exceeds limit")
	}
	if count <= 0 {
		count = 1
	}
	rg := d.rateGroupForStep(lotteryID, step)
	if rg == 0 {
		rg = info.RateGroupID
	}
	out := make([]Reward, 0, count)
	for i := 0; i < count; i++ {
		r, err := d.rollOne(rg, intn)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// CardID resolves a MasterCard.label (e.g. "RKayamori01") to its id.
func (d *Data) CardID(label string) (int64, bool) {
	if d == nil || label == "" || d.cardByLbl == nil {
		return 0, false
	}
	id, ok := d.cardByLbl[label]
	return id, ok
}

// LotteryConsumeChargeGem is LotteryConsumeType.ChargeGem — a gem-paid draw.
// Other observed consumeTypes: 0 free, 2 ticket, 4 termStockItem.
const LotteryConsumeChargeGem = 1

// LotteryConsume reports how a lottery draw is paid for: the consume type and
// the gem cost, read from the MasterLottery row. For a variable-draw-count
// lottery the gem cost scales proportionally to the requested rewardCount over
// the master row's rewardCount. ok is false when the lottery id is unknown.
//
// NOTE: the proportional scaling for isVariableDrawCount is an assumption to be
// confirmed against a real gem lottery in MasterLottery; the local tutorial
// lotteries are all gemCost:0 free/ticket draws.
func (d *Data) LotteryConsume(lotteryID int64, rewardCount int) (consumeType, gemCost int64, ok bool) {
	row, found := d.lotteryByID[lotteryID]
	if !found {
		return 0, 0, false
	}
	consumeType, _ = num(row["consumeType"])
	gemCost, _ = num(row["gemCost"])
	if variable, _ := row["isVariableDrawCount"].(bool); variable && rewardCount > 0 {
		if base, ok2 := num(row["rewardCount"]); ok2 && base > 0 {
			gemCost = gemCost * int64(rewardCount) / base
		}
	}
	return consumeType, gemCost, true
}

// CardConvert reports the style-piece a duplicate card converts into
// (MasterCard.masterItemLabel → MasterItem). Official draws always grant
// CardDuplicatePieceNum of that item and set convertedItemLabel to the label.
func (d *Data) CardConvert(cardID int64) (label string, itemID int64, ok bool) {
	if d == nil || d.cardConvert == nil {
		return "", 0, false
	}
	c, ok := d.cardConvert[cardID]
	return c.Label, c.ItemID, ok
}

// CardDuplicatePieceNum is how many style pieces a duplicated card becomes.
// Official 1-pull and 10-pull conversions all grant this amount.
const CardDuplicatePieceNum = 10

// LotteryTicket reports the ticket item a lottery consumes (MasterLottery.consumeItemId
// + ticketCost). ok is false when the lottery is unknown or not ticket-paid.
func (d *Data) LotteryTicket(lotteryID int64) (itemID, cost int64, ok bool) {
	row, found := d.lotteryByID[lotteryID]
	if !found {
		return 0, 0, false
	}
	itemID, _ = num(row["consumeItemId"])
	cost, _ = num(row["ticketCost"])
	return itemID, cost, itemID > 0 && cost > 0
}

// TermStockEffectiveDays is MasterTermStockItem.effectiveDayNum (fallback 30).
func (d *Data) TermStockEffectiveDays(id int64) int64 {
	const fallback = 30
	if d == nil {
		return fallback
	}
	f, ok := d.File("MasterTermStockItem")
	if !ok {
		return fallback
	}
	row, ok := f.ByID[id]
	if !ok {
		return fallback
	}
	n, ok := num(row["effectiveDayNum"])
	if !ok || n <= 0 {
		return fallback
	}
	return n
}

// TermStockItemByLabel resolves a MasterTermStockItem by label (a term-stock
// reward's masterLabel, e.g. "StockItem.ticket.2") to its id and validity
// period in days.
func (d *Data) TermStockItemByLabel(label string) (id, effectiveDays int64, ok bool) {
	if d == nil || label == "" {
		return 0, 0, false
	}
	f, found := d.File("MasterTermStockItem")
	if !found {
		return 0, 0, false
	}
	for _, r := range f.Items {
		if lbl, _ := r["label"].(string); lbl != label {
			continue
		}
		id, _ = num(r["id"])
		effectiveDays, _ = num(r["effectiveDayNum"])
		if effectiveDays <= 0 {
			effectiveDays = 30
		}
		return id, effectiveDays, id > 0
	}
	return 0, 0, false
}
