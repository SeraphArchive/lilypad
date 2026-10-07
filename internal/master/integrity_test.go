package master

import "testing"

func TestBatchDropLimitAndAllocationBound(t *testing.T) {
	d := syntheticRewards()
	d.files["MasterItemLottery"] = &File{ByID: map[int64]Row{400: {"id": 400, "lotteryRewardGroupLabel": "pool"}}}
	d.files["MasterItemLotteryReward"] = &File{Items: []Row{{"id": 401, "groupLabel": "pool", "ratio": 1, "dropLimit": 1, "rewardGroupLabel": "Reward.M1"}}}
	roll, err := d.RollItemLottery(400, 3, map[int64]int64{}, func(int) int { return 0 })
	if err != nil || len(roll) != 1 {
		t.Fatal("remaining pool must yield only one draw", roll, err)
	}
	if _, err := synthetic().Roll(100, MaxDrawCount+1, func(int) int { return 0 }); err == nil {
		t.Fatal("unbounded count accepted")
	}
}

func TestPickupGuaranteeCompletionAndReset(t *testing.T) {
	d := syntheticRewards()
	d.files["MasterItemLottery"] = &File{ByID: map[int64]Row{400: {"id": 400, "lotteryRewardGroupLabel": "pool", "guaranteedPickupDrawCount": 2, "afterPickupCompleteType": 1, "completeRewardGroupLabel": "Reward.M1"}}}
	d.files["MasterItemLotteryReward"] = &File{Items: []Row{{"id": 401, "groupLabel": "pool", "ratio": 100, "rewardGroupLabel": "Reward.M1"}, {"id": 402, "groupLabel": "pool", "ratio": 1, "isPickup": true, "dropLimit": 1, "rewardGroupLabel": "Reward.M1"}}}
	roll, err := d.RollItemLotteryState(400, 10, nil, 0, func(int) int { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	if len(roll.Picks) != 2 || roll.Picks[0].IsPickup || !roll.Picks[1].IsPickup || !roll.Terminated || len(roll.Picks[1].CompleteRewards) == 0 || roll.NonPickupCount != 0 {
		t.Fatal(roll)
	}
	d.files["MasterItemLottery"].ByID[400]["afterPickupCompleteType"] = 2
	roll, err = d.RollItemLotteryState(400, 4, nil, 0, func(int) int { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	if len(roll.Picks) != 4 || roll.Terminated || roll.ResetCount != 2 || roll.DropCounts[402] != 0 {
		t.Fatal(roll)
	}
}
