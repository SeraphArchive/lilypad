package master

import "testing"

func TestGemGrants(t *testing.T) {
	d := syntheticRewards() // Reward.M1 includes {category:99, num:20, masterLabel:""}
	if got := d.GemGrants("Reward.M1"); got != 20 {
		t.Fatalf("GemGrants: got %d want 20", got)
	}
	if got := d.GemGrants("Reward.DoesNotExist"); got != 0 {
		t.Fatalf("GemGrants unknown: got %d want 0", got)
	}
}

func syntheticLotteryCosts() *Data {
	d := &Data{
		files:         map[string]*File{},
		lotteryByID:   map[int64]Row{},
		rateByGroup:   map[int64][]Row{},
		rewardByGroup: map[int64][]Row{},
	}
	d.files["MasterLottery"] = &File{ByID: map[int64]Row{
		62000001: {"id": 62000001, "consumeType": 1, "gemCost": 300, "rewardCount": 1},
		62000010: {"id": 62000010, "consumeType": 1, "gemCost": 3000, "rewardCount": 10, "isVariableDrawCount": true},
		50000000: {"id": 50000000, "consumeType": 0, "gemCost": 0, "rewardCount": 1},
	}}
	d.buildIndices()
	return d
}

func TestLotteryConsume(t *testing.T) {
	d := syntheticLotteryCosts()

	// Fixed gem single.
	ct, cost, ok := d.LotteryConsume(62000001, 1)
	if !ok || ct != LotteryConsumeChargeGem || cost != 300 {
		t.Fatalf("single gem: ct=%d cost=%d ok=%v", ct, cost, ok)
	}
	// Free draw.
	ct, cost, ok = d.LotteryConsume(50000000, 1)
	if !ok || ct != 0 || cost != 0 {
		t.Fatalf("free: ct=%d cost=%d ok=%v", ct, cost, ok)
	}
	// Variable count scales proportionally to the master rewardCount.
	_, cost, _ = d.LotteryConsume(62000010, 10)
	if cost != 3000 {
		t.Fatalf("variable x10: cost=%d want 3000", cost)
	}
	_, cost, _ = d.LotteryConsume(62000010, 1)
	if cost != 300 {
		t.Fatalf("variable x1: cost=%d want 300", cost)
	}
	// Unknown lottery.
	if _, _, ok := d.LotteryConsume(999999, 1); ok {
		t.Fatal("unknown lottery should be ok=false")
	}
}
