package master

import (
	"os"
	"testing"
	"time"
)

func TestIsOpenedWindows(t *testing.T) {
	dir := os.Getenv("LILYPAD_DATA_DIR")
	if dir == "" {
		t.Skip("LILYPAD_DATA_DIR not set")
	}
	d, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 2026-06-15 22:25 JST: P5 collab login bonus is open; 2026-08-13 23:31 JST it is not.
	newPlayer := time.Date(2026, 6, 15, 22, 25, 17, 0, jst).Unix()
	late := time.Date(2026, 8, 13, 23, 31, 52, 0, jst).Unix()
	if !d.IsOpened("release.loginbonus_CC0005", newPlayer) {
		t.Fatal("CC0005 should be open on 2026-06-15")
	}
	if d.IsOpened("release.loginbonus_CC0005", late) {
		t.Fatal("CC0005 should be closed on 2026-08-13")
	}
	if !d.IsOpened("release.default", late) {
		t.Fatal("release.default always open")
	}
	if !d.IsOpened("release.loginbornus_Startdash", late) {
		t.Fatal("start-dash has no closeAt")
	}
	if d.IsOpened("release.TC0001", late) {
		t.Fatal("Christmas garden 2022 window must be closed in 2026")
	}
	if !d.IsOpened("release.weekly.thu", late) {
		t.Fatal("Thursday 23:31 JST is still Thursday after the 4h offset")
	}
	// Thursday 03:00 JST is still Wednesday after -4h.
	thu0300 := time.Date(2026, 8, 13, 3, 0, 0, 0, jst).Unix()
	if d.IsOpened("release.weekly.thu", thu0300) {
		t.Fatal("Thursday 03:00 JST should still be Wednesday weekly")
	}
}

func TestOpenGardenDropIDsSkipsClosedEvents(t *testing.T) {
	dir := os.Getenv("LILYPAD_DATA_DIR")
	if dir == "" {
		t.Skip("LILYPAD_DATA_DIR not set")
	}
	d, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	late := time.Date(2026, 8, 13, 23, 31, 52, 0, jst).Unix()
	ids := d.OpenGardenDropIDs(late)
	for _, id := range ids {
		if id >= 83000009 {
			t.Fatalf("closed TC0001 drop %d leaked: %v", id, ids)
		}
	}
	if len(ids) < 7 {
		t.Fatalf("evergreen garden drops = %v", ids)
	}
}

func TestIsOpenedForPlatform(t *testing.T) {
	dir := os.Getenv("LILYPAD_DATA_DIR")
	if dir == "" {
		t.Skip("LILYPAD_DATA_DIR not set")
	}
	d, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 23, 0, 0, 0, jst).Unix()
	if !d.IsOpenedFor("release.pc", now, PlatformSteam, 0) {
		t.Fatal("release.pc is Steam-only and should open for platform=4")
	}
	if d.IsOpenedFor("release.pc", now, 1, 0) {
		t.Fatal("release.pc must stay closed for Android")
	}
	if !d.IsOpenedFor("release.default", now, PlatformSteam, 1) {
		t.Fatal("default ignores platform/region")
	}
}
