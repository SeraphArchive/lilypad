package httpapi

import (
	"testing"

	"lilypad/internal/deltacomm"
	"lilypad/internal/master"
)

type fakeConvert map[int64]struct {
	label  string
	itemID int64
}

func (f fakeConvert) CardConvert(cardID int64) (string, int64, bool) {
	c, ok := f[cardID]
	return c.label, c.itemID, ok
}

func TestGrantLotteryRewardsConvertsDuplicates(t *testing.T) {
	md := fakeConvert{
		1001: {label: "Item_A", itemID: 11},
		2001: {label: "Item_B", itemID: 22},
	}

	owned := map[int64]bool{1001: true}
	rewards := []master.Reward{
		{Category: master.RewardCategoryCard, RewardID: 1001}, // dup
		{Category: master.RewardCategoryCard, RewardID: 2001}, // new
		{Category: master.RewardCategoryCard, RewardID: 2001}, // same-draw dup
	}
	cards, results, items, _ := grantLotteryRewards(md, owned, 62, rewards, 10)
	if len(cards) != 1 || asInt(cards[0]["_masterCardId"]) != 2001 {
		t.Fatalf("new cards = %v", cards)
	}
	if len(results) != 3 {
		t.Fatalf("results = %d", len(results))
	}
	if results[0]["_isDuplicated"] != true || results[0]["_convertedItemLabel"] != "Item_A" {
		t.Fatalf("first result: %v", results[0])
	}
	if results[1]["_isDuplicated"] != false || results[1]["_convertedItemLabel"] != "" {
		t.Fatalf("second result: %v", results[1])
	}
	if results[2]["_isDuplicated"] != true || results[2]["_convertedItemLabel"] != "Item_B" {
		t.Fatalf("third result: %v", results[2])
	}
	if items[11] != master.CardDuplicatePieceNum || items[22] != master.CardDuplicatePieceNum {
		t.Fatalf("piece grants = %v", items)
	}
}

func TestNextLotteryRowIncrements(t *testing.T) {
	first, err := nextLotteryRow(nil, 41, 10, 100)
	if err != nil {
		t.Fatal(err)
	}
	if asInt(first["_step"]) != 1 {
		t.Fatalf("first step: %v", first)
	}
	if asInt(first["_drawCount"]) != 1 || asInt(first["_issueRewardCount"]) != 10 {
		t.Fatalf("first: %v", first)
	}
	second, err := nextLotteryRow([]deltacomm.Row{first}, 41, 10, 200)
	if err != nil {
		t.Fatal(err)
	}
	if asInt(second["_drawCount"]) != 2 || asInt(second["_dailyDrawCount"]) != 2 || asInt(second["_issueRewardCount"]) != 20 {
		t.Fatalf("second: %v", second)
	}
}

func TestConsumeTermStock(t *testing.T) {
	rows := []deltacomm.Row{
		{"_uid": "aaa", "_value": int64(1), "_termStockItemId": int64(159)},
		{"_uid": "bbb", "_value": int64(1), "_termStockItemId": int64(159)},
	}
	out := consumeTermStock(rows, []string{"aaa"}, 1)
	if len(out) != 1 || asInt(out[0]["_value"]) != 0 || out[0]["_uid"] != "aaa" {
		t.Fatalf("consume: %v", out)
	}
	if asInt(rows[1]["_value"]) != 1 {
		t.Fatal("untouched uid mutated")
	}
}

func TestApplyItemConsumeLeavesTotalNum(t *testing.T) {
	cur := []deltacomm.Row{{"_id": int64(7040001), "_num": int64(1), "_totalNum": int64(1), "_reservedNum": 0, "_isAlreadyPossessed": false, "_isLocked": false}}
	out := applyItemConsume(cur, 7040001, 1)
	if len(out) != 1 || asInt(out[0]["_num"]) != 0 || asInt(out[0]["_totalNum"]) != 1 {
		t.Fatalf("ticket leftover: %v", out[0])
	}
}

func TestApplyItemConsumeInventedRowKeepsTotalNum(t *testing.T) {
	out := applyItemConsume(nil, 7040001, 1)
	if len(out) != 1 || asInt(out[0]["_num"]) != 0 || asInt(out[0]["_totalNum"]) != 1 {
		t.Fatalf("invented leftover: %v", out)
	}
}
