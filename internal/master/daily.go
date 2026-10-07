package master

// LoginBonusDef is one MasterLoginBonus row plus its content days.
type LoginBonusDef struct {
	ID           int64
	ContentGroup string
	ReleaseLabel string
	CanLoop      bool
	TimeLimitSec int64
	Days         []LoginBonusDay
}

// LoginBonusDay is one MasterLoginBonusContent row.
type LoginBonusDay struct {
	Day         int64
	RewardGroup string
}

// LoginBonusReward is one MasterReward row granted by a login-bonus day.
type LoginBonusReward struct {
	ID          int64
	Category    int64
	ItemID      int64
	Num         int64
	ReasonID    int64
	MasterLabel string
}

// LoginBonuses returns every defined login bonus (release gating is the caller's job).
func (d *Data) LoginBonuses() []LoginBonusDef {
	if d == nil {
		return nil
	}
	f, ok := d.File("MasterLoginBonus")
	if !ok {
		return nil
	}
	cf, _ := d.File("MasterLoginBonusContent")
	byGroup := map[string][]LoginBonusDay{}
	if cf != nil {
		for _, r := range cf.Items {
			grp, _ := r["groupLabel"].(string)
			if grp == "" {
				continue
			}
			day, _ := num(r["day"])
			rew, _ := r["rewardGroupLabel"].(string)
			byGroup[grp] = append(byGroup[grp], LoginBonusDay{Day: day, RewardGroup: rew})
		}
	}
	out := make([]LoginBonusDef, 0, len(f.ByID))
	for id, r := range f.ByID {
		grp, _ := r["loginBonusContentGroupLabel"].(string)
		rel, _ := r["releaseLabel"].(string)
		canLoop, _ := r["canLoop"].(bool)
		limit, _ := num(r["timeLimitFromRegistered"])
		out = append(out, LoginBonusDef{
			ID: id, ContentGroup: grp, ReleaseLabel: rel, CanLoop: canLoop, TimeLimitSec: limit, Days: byGroup[grp],
		})
	}
	return out
}

// ContentForDay picks the content row for a 1-based day. Looping bonuses wrap.
func (b LoginBonusDef) ContentForDay(day int64) (LoginBonusDay, bool) {
	if day <= 0 || len(b.Days) == 0 {
		return LoginBonusDay{}, false
	}
	max := int64(0)
	var hit LoginBonusDay
	var found bool
	for _, c := range b.Days {
		if c.Day > max {
			max = c.Day
		}
		if c.Day == day {
			hit, found = c, true
		}
	}
	if found {
		return hit, true
	}
	if max <= 0 {
		return LoginBonusDay{}, false
	}
	if day <= max && !b.CanLoop {
		return LoginBonusDay{}, false
	}
	if !b.CanLoop {
		return LoginBonusDay{}, false
	}
	wrapped := ((day - 1) % max) + 1
	for _, c := range b.Days {
		if c.Day == wrapped {
			return c, true
		}
	}
	return LoginBonusDay{}, false
}

// LoginBonusRewards expands a reward-group label into concrete grant rows.
func (d *Data) LoginBonusRewards(groupLabel string) []LoginBonusReward {
	if d == nil || groupLabel == "" {
		return nil
	}
	rows := d.rewardByLabel[groupLabel]
	out := make([]LoginBonusReward, 0, len(rows))
	for _, r := range rows {
		id, _ := num(r["id"])
		cat, _ := num(r["category"])
		n, _ := num(r["num"])
		lbl, _ := r["masterLabel"].(string)
		reasonLbl, _ := r["masterReasonLabel"].(string)
		rw := LoginBonusReward{ID: id, Category: cat, Num: n, MasterLabel: lbl}
		if reasonLbl != "" {
			if rid, ok := d.ReasonID(reasonLbl); ok {
				rw.ReasonID = rid
			}
		}
		if lbl != "" {
			if iid, ok := d.ItemID(lbl); ok {
				rw.ItemID = iid
			}
		}
		out = append(out, rw)
	}
	return out
}

// DailyMissionsForWeekday returns the missionCategory=1 missions that reset on
// this weekday. weekday is time.Weekday (Sunday=0). The "clear all dailies"
// mission (release.default) is always included.
func (d *Data) DailyMissionsForWeekday(weekday int) []int64 {
	if d == nil {
		return nil
	}
	f, ok := d.File("MasterMission")
	if !ok {
		return nil
	}
	want := weekdayRelease[weekday]
	var out []int64
	for _, r := range f.Items {
		cat, _ := num(r["missionCategory"])
		if cat != 1 {
			continue
		}
		rel, _ := r["releaseLabel"].(string)
		if rel != "release.default" && rel != want {
			continue
		}
		id, ok := num(r["id"])
		if ok {
			out = append(out, id)
		}
	}
	return out
}

var weekdayRelease = map[int]string{
	0: "release.weekly.sun",
	1: "release.weekly.mon",
	2: "release.weekly.tue",
	3: "release.weekly.wed",
	4: "release.weekly.thu",
	5: "release.weekly.fri",
	6: "release.weekly.sat",
}

// DefaultGardenDropIDs is the evergreen (release.default) garden drop set.
func (d *Data) DefaultGardenDropIDs() []int64 {
	if d == nil {
		return nil
	}
	f, ok := d.File("MasterGardenDropItem")
	if !ok {
		return nil
	}
	var out []int64
	for _, r := range f.Items {
		rel, _ := r["releaseLabel"].(string)
		if rel != "release.default" {
			continue
		}
		id, ok := num(r["id"])
		if ok {
			out = append(out, id)
		}
	}
	return out
}

// LotteryPointItemID returns the banner-point item for a lottery, matched by
// the shop's releaseLabel against MasterItem.exchangeReleaseLabel
// (Lottery_Point* / category 13).
func (d *Data) LotteryPointItemID(lotteryID int64) (int64, bool) {
	if d == nil {
		return 0, false
	}
	lot, ok := d.lotteryByID[lotteryID]
	if !ok {
		return 0, false
	}
	shopID, ok := num(lot["masterLotteryShopId"])
	if !ok || shopID == 0 {
		return 0, false
	}
	sf, ok := d.File("MasterLotteryShop")
	if !ok {
		return 0, false
	}
	shop, ok := sf.ByID[shopID]
	if !ok {
		return 0, false
	}
	rel, _ := shop["releaseLabel"].(string)
	if rel == "" {
		return 0, false
	}
	f, ok := d.File("MasterItem")
	if !ok {
		return 0, false
	}
	for _, r := range f.Items {
		lbl, _ := r["label"].(string)
		if !stringsHasPrefix(lbl, "Lottery_Point") {
			continue
		}
		ex, _ := r["exchangeReleaseLabel"].(string)
		if ex == rel {
			id, ok := num(r["id"])
			return id, ok
		}
	}
	return 0, false
}

func stringsHasPrefix(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}

// ReasonID resolves a MasterReason label.
func (d *Data) ReasonID(label string) (int64, bool) {
	if d == nil || label == "" {
		return 0, false
	}
	f, ok := d.File("MasterReason")
	if !ok {
		return 0, false
	}
	for _, r := range f.Items {
		if s, _ := r["label"].(string); s == label {
			id, ok := num(r["id"])
			return id, ok
		}
	}
	return 0, false
}
