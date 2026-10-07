package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"lilypad/internal/codec"
	"lilypad/internal/config"
	"lilypad/internal/sign"
)

func TestNewRoutesReturnSyncEnvelope(t *testing.T) {
	st := newFakeStore()
	s := New(&config.Config{}, sign.NoopSigner{}, st, st, nil, nil)
	wire, err := codec.Encode(map[string]any{"hdr": "", "dummy": 0, "recoveryNum": 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/lottery/prepare",
		"/api/stamina/recover",
		"/api/spirit/recover",
		"/api/life/recover",
		"/api/user/profile",
		"/api/live/ranking",
		"/api/arcade/ranking/list",
		"/api/wave_battle/ranking/list",
		"/api/octopus/hunting/ranking/list",
		"/api/user/other/arcade/result/fetch",
		"/api/user/other/wave_battle/result/fetch",
	} {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(wire))
		req.Header.Set("x-player-id", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%q", path, rec.Code, rec.Body.String())
		}
		out, err := codec.Decode(rec.Body.Bytes())
		if err != nil {
			t.Fatalf("%s decode: %v", path, err)
		}
		if _, ok := out["tables"]; !ok {
			t.Fatalf("%s missing tables: %v", path, out)
		}
		if _, ok := out["hashes"]; !ok {
			t.Fatalf("%s missing hashes: %v", path, out)
		}
		tbls, _ := out["tables"].(map[string]any)
		if tbls != nil {
			if _, ok := tbls["putItems"]; !ok {
				t.Fatalf("%s tables missing putItems: %v", path, tbls)
			}
			if _, ok := tbls["replaceItems"]; !ok {
				t.Fatalf("%s tables missing replaceItems: %v", path, tbls)
			}
		}
		_ = json.Number("0")
	}
}

func TestDailyTimestampFieldOmitsAllClear(t *testing.T) {
	if dailyTimestampField("_lastAllDailyMissionClearedAt") {
		t.Fatal("daily/update must not stamp _lastAllDailyMissionClearedAt")
	}
	if !dailyTimestampField("_lastDailyUpdatedAt") {
		t.Fatal("_lastDailyUpdatedAt should still reset")
	}
}
