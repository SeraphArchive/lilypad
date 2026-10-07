package httpapi

import (
	"crypto/rand"
	"errors"
	"math/big"
	"time"

	"lilypad/internal/deltacomm"
	"lilypad/internal/master"
)

const (
	jstOffsetSec     = 9 * 3600
	dailyResetHour   = 4
	gardenDropReady  = 2
	recordLoginDays  = 7
	recordLoginKindA = 13
	recordLoginKindB = 36
)

// jstDailyReset is the most recent JST 04:00 at or before now.
func jstDailyReset(now int64) int64 {
	t := time.Unix(now, 0).In(time.FixedZone("JST", jstOffsetSec))
	reset := time.Date(t.Year(), t.Month(), t.Day(), dailyResetHour, 0, 0, 0, t.Location())
	if t.Before(reset) {
		reset = reset.Add(-24 * time.Hour)
	}
	return reset.Unix()
}

func jstNextDailyReset(now int64) int64 {
	return jstDailyReset(now) + 24*3600
}

func jstWeekday(now int64) time.Weekday {
	return time.Unix(jstDailyReset(now), 0).In(time.FixedZone("JST", jstOffsetSec)).Weekday()
}

func alreadyDailyUpdated(profile deltacomm.Row, now int64) bool {
	if profile == nil {
		return false
	}
	last := asInt(profile["_lastDailyUpdatedAt"])
	return last >= jstDailyReset(now)
}

func nextLoginBonusProgress(prev deltacomm.Row, bonus master.LoginBonusDef) (day, lap int64, ok bool) {
	max := int64(0)
	for _, c := range bonus.Days {
		if c.Day > max {
			max = c.Day
		}
	}
	if max <= 0 {
		return 0, 0, false
	}
	if prev == nil {
		return 1, 0, true
	}
	day = asInt(prev["_lastReceivedDay"]) + 1
	lap = asInt(prev["_lastReceivedLap"])
	if day > max {
		if !bonus.CanLoop {
			return 0, 0, false
		}
		day = 1
		lap++
	}
	return day, lap, true
}

func newGiftID() int64 {
	n, err := rand.Int(rand.Reader, big.NewInt(9_000_000_000_000_000))
	if err != nil {
		return time.Now().UnixNano() & 0x3fffffffffffff
	}
	return n.Int64() + 1_000_000_000_000_000
}

func loginBonusGifts(md *master.Data, bonus master.LoginBonusDef, day, now int64) ([]deltacomm.Row, error) {
	content, ok := bonus.ContentForDay(day)
	if !ok || md == nil {
		return nil, errors.New("missing login bonus day")
	}
	rewards := md.LoginBonusRewards(content.RewardGroup)
	if len(rewards) == 0 {
		return nil, errors.New("missing login bonus reward group")
	}
	var out []deltacomm.Row
	for _, rw := range rewards {
		if rw.Num <= 0 {
			return nil, errors.New("invalid login bonus reward amount")
		}
		switch rw.Category {
		case rewardCategoryItem:
			if rw.ItemID <= 0 {
				return nil, errors.New("missing login bonus item")
			}
		case rewardCategoryTermStock:
			var ok bool
			rw.ItemID, _, ok = md.TermStockItemByLabel(rw.MasterLabel)
			if !ok {
				return nil, errors.New("missing login bonus term-stock item")
			}
		case master.RewardCategoryHardCurrency:
		default:
			return nil, errors.New("unsupported login bonus reward category")
		}
		row := deltacomm.Row{
			"_giftId":         newGiftID(),
			"_rewardCategory": rw.Category,
			"_itemId":         rw.ItemID,
			"_itemNum":        rw.Num,
			"_masterReasonId": rw.ReasonID,
			"_masterRewardId": rw.ID,
			"_isReceived":     false,
			"_registeredAt":   now,
			"_startAt":        now,
			"_expiredAt":      now + 30*86400,
			"_receivedAt":     0,
		}
		out = append(out, row)
	}
	return out, nil
}

func incrementRecord(prev []deltacomm.Row, typ int64) deltacomm.Row {
	val := int64(1)
	for _, r := range prev {
		if asInt(r["_type"]) == typ {
			val = asInt(r["_value"]) + 1
			break
		}
	}
	return deltacomm.Row{"_type": typ, "_value": val}
}
