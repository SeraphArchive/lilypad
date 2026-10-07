package httpapi

import (
	"testing"
	"time"

	"lilypad/internal/deltacomm"
	"lilypad/internal/master"
)

func TestJSTDailyReset(t *testing.T) {
	// 2026-08-13 23:31:52 JST → reset at 2026-08-13 04:00 JST
	now := int64(1786631512)
	got := jstDailyReset(now)
	want := time.Date(2026, 8, 13, 4, 0, 0, 0, time.FixedZone("JST", jstOffsetSec)).Unix()
	if got != want {
		t.Fatalf("reset=%d want %d", got, want)
	}
	if jstNextDailyReset(now) != 1786647600 {
		t.Fatalf("next expire = %d want 1786647600", jstNextDailyReset(now))
	}
	if jstWeekday(now) != time.Thursday {
		t.Fatalf("weekday = %v", jstWeekday(now))
	}
}

func TestAlreadyDailyUpdated(t *testing.T) {
	now := int64(1786631512)
	reset := jstDailyReset(now)
	if !alreadyDailyUpdated(deltacomm.Row{"_lastDailyUpdatedAt": reset + 10}, now) {
		t.Fatal("same-day stamp must skip")
	}
	if alreadyDailyUpdated(deltacomm.Row{"_lastDailyUpdatedAt": reset - 10}, now) {
		t.Fatal("yesterday stamp must not skip")
	}
}

func TestNextLoginBonusProgress(t *testing.T) {
	bonus := master.LoginBonusDef{
		CanLoop: true,
		Days:    []master.LoginBonusDay{{Day: 1}, {Day: 2}, {Day: 7}},
	}
	day, lap, ok := nextLoginBonusProgress(nil, bonus)
	if !ok || day != 1 || lap != 0 {
		t.Fatalf("first = %d %d %v", day, lap, ok)
	}
	day, lap, ok = nextLoginBonusProgress(deltacomm.Row{"_lastReceivedDay": int64(6), "_lastReceivedLap": int64(104)}, bonus)
	if !ok || day != 7 || lap != 104 {
		t.Fatalf("day6->7 = %d %d %v", day, lap, ok)
	}
	day, lap, ok = nextLoginBonusProgress(deltacomm.Row{"_lastReceivedDay": int64(7), "_lastReceivedLap": int64(104)}, bonus)
	if !ok || day != 1 || lap != 105 {
		t.Fatalf("wrap = %d %d %v", day, lap, ok)
	}
	finite := master.LoginBonusDef{CanLoop: false, Days: []master.LoginBonusDay{{Day: 1}, {Day: 14}}}
	if _, _, ok := nextLoginBonusProgress(deltacomm.Row{"_lastReceivedDay": int64(14)}, finite); ok {
		t.Fatal("finished finite bonus must stop")
	}
}

func TestLoginBonusContentWrap(t *testing.T) {
	b := master.LoginBonusDef{
		CanLoop: true,
		Days: []master.LoginBonusDay{
			{Day: 1, RewardGroup: "A"},
			{Day: 7, RewardGroup: "G"},
		},
	}
	c, ok := b.ContentForDay(8)
	if !ok || c.Day != 1 || c.RewardGroup != "A" {
		t.Fatalf("wrap day8 = %+v %v", c, ok)
	}
	c, ok = b.ContentForDay(7)
	if !ok || c.RewardGroup != "G" {
		t.Fatalf("day7 = %+v %v", c, ok)
	}
}
