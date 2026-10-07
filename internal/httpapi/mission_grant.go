package httpapi

import (
	"errors"
	"math"

	"lilypad/internal/master"
)

// Mission settlement must account for every master reward before marking it
// received. Missing definitions or unsupported grants keep the claim pending.
func validateMissionRewards(md *master.Data, group string, times int64, hasProfile, weeklyPoints bool) error {
	rewards := md.RewardGroup(group)
	if len(rewards) == 0 {
		return errors.New("missing mission reward group")
	}
	for _, reward := range rewards {
		if reward.Num < 0 || times <= 0 || reward.Num > math.MaxInt64/times {
			return errors.New("invalid mission reward amount")
		}
		switch reward.Category {
		case rewardCategoryItem:
			if _, ok := md.ItemID(reward.MasterLabel); !ok {
				return errors.New("missing mission item")
			}
		case 10:
			if _, ok := md.CurrencyID(reward.MasterLabel); !ok {
				return errors.New("missing mission currency")
			}
		case master.RewardCategoryHardCurrency:
		case 13:
			if !hasProfile {
				return errors.New("missing mission reward profile")
			}
		case 22:
			// Weekly gauge points are derived by the client from received regular
			// missions and their master rewards, rather than an inventory field.
			if !weeklyPoints {
				return errors.New("unsupported weekly points for loop mission")
			}
		default:
			return errors.New("unsupported mission reward category")
		}
	}
	return nil
}

func accumulateMissionRewards(md *master.Data, group string, times int64, hasProfile, weeklyPoints bool, currencies, items map[int64]int64, profile map[string]int64, gems *int64) error {
	if err := validateMissionRewards(md, group, times, hasProfile, weeklyPoints); err != nil {
		return err
	}
	for _, reward := range md.RewardGroup(group) {
		amount := reward.Num * times
		var err error
		switch reward.Category {
		case rewardCategoryItem:
			id, _ := md.ItemID(reward.MasterLabel)
			err = accumulateReward(items, id, amount)
		case 10:
			id, _ := md.CurrencyID(reward.MasterLabel)
			err = accumulateReward(currencies, id, amount)
		case 13:
			err = accumulateReward(profile, "_limitBreakPower", amount)
		case master.RewardCategoryHardCurrency:
			*gems, err = addReward(*gems, amount)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
