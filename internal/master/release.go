package master

import (
	"time"
)

const releaseTimeLayout = "2006/01/02 15:04:05"

// jst is the zone MasterRelease openAt/closeAt strings are written in
// (02:00 maintenance windows, 4h weekly offset).
var jst = time.FixedZone("JST", 9*3600)

// IsOpened reports whether a MasterRelease label is active at now (unix seconds).
// Empty labels and "release.default" are always open. Missing rows are closed.
// Weekly labels also consult MasterReleasePeriod (local day + offset hours).
func (d *Data) IsOpened(label string, now int64) bool {
	return d.IsOpenedFor(label, now, 0, 0)
}

// IsOpenedFor is IsOpened with a platform / region filter.
// platform/region 0 on the caller means "don't care". A row with platform/region 0
// matches every caller. Non-zero values must equal (Steam=4, JP=1, AP=2, …).
func (d *Data) IsOpenedFor(label string, now int64, platform, region int64) bool {
	if d == nil || label == "" || label == "release.default" {
		return true
	}
	f, ok := d.File("MasterRelease")
	if !ok {
		return false
	}
	var row Row
	found := false
	for _, r := range f.Items {
		if s, _ := r["label"].(string); s == label {
			row, found = r, true
			break
		}
	}
	if !found {
		return false
	}
	if plat, ok := num(row["platform"]); ok && plat != 0 && platform != 0 && plat != platform {
		return false
	}
	if reg, ok := num(row["region"]); ok && reg != 0 && region != 0 && reg != region {
		return false
	}
	t := time.Unix(now, 0).In(jst)
	if open, ok := parseReleaseTime(row["openAt"]); ok && t.Before(open) {
		return false
	}
	if close, ok := parseReleaseTime(row["closeAt"]); ok && t.After(close) {
		return false
	}
	return d.releasePeriodOpen(label, t)
}

func parseReleaseTime(v any) (time.Time, bool) {
	s, _ := v.(string)
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(releaseTimeLayout, s, jst)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func (d *Data) releasePeriodOpen(label string, t time.Time) bool {
	f, ok := d.File("MasterReleasePeriod")
	if !ok {
		return true
	}
	var periods []Row
	for _, r := range f.Items {
		if s, _ := r["releaseLabel"].(string); s == label {
			periods = append(periods, r)
		}
	}
	if len(periods) == 0 {
		return true
	}
	for _, p := range periods {
		if periodMatches(p, t) {
			return true
		}
	}
	return false
}

func periodMatches(p Row, t time.Time) bool {
	week, _ := num(p["week"])
	if week == 0 {
		return true
	}
	offset, _ := num(p["offset"])
	shifted := t
	if isLocal, _ := p["isLocal"].(bool); isLocal {
		shifted = t.Add(-time.Duration(offset) * time.Hour)
	}
	bit := int64(1) << uint(shifted.Weekday()) // Sunday=0 → 1, Monday=1 → 2, …
	return week&bit != 0
}

// OpenGardenDropIDs returns garden drops whose release label is open at now.
func (d *Data) OpenGardenDropIDs(now int64) []int64 {
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
		if !d.IsOpened(rel, now) {
			continue
		}
		id, ok := num(r["id"])
		if ok {
			out = append(out, id)
		}
	}
	return out
}

// ReleaseOpenAt returns a MasterRelease row's openAt as unix seconds.
// ok is false when the label is unknown or has no openAt.
func (d *Data) ReleaseOpenAt(label string) (int64, bool) {
	if d == nil || label == "" {
		return 0, false
	}
	f, ok := d.File("MasterRelease")
	if !ok {
		return 0, false
	}
	for _, r := range f.Items {
		if s, _ := r["label"].(string); s != label {
			continue
		}
		t, ok := parseReleaseTime(r["openAt"])
		if !ok {
			return 0, false
		}
		return t.Unix(), true
	}
	return 0, false
}
