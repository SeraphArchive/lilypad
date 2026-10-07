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

// TestLotteryPrepareReturnsSyncEnvelope pins the gacha-open fix: the client
// sends /api/lottery/prepare with ignoreSyncData=false, so GameServerRequest
// treats a body missing "tables"/"hashes" as a failure (OnFailed → the E0
// network-error dialog). The handler must return the full envelope.
func TestLotteryPrepareReturnsSyncEnvelope(t *testing.T) {
	st := newFakeStore()
	s := New(&config.Config{}, sign.NoopSigner{}, st, st, nil, nil)

	wire, err := codec.Encode(map[string]any{"hdr": "", "dummy": 0})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/lottery/prepare", bytes.NewReader(wire))
	req.Header.Set("x-player-id", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	out, err := codec.Decode(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if c, _ := out["code"].(json.Number); c.String() != "0" {
		t.Fatalf("code=%v", out["code"])
	}
	if _, ok := out["tables"]; !ok {
		t.Fatalf("response missing tables: %v", out)
	}
	if _, ok := out["hashes"]; !ok {
		t.Fatalf("response missing hashes: %v", out)
	}
	if _, ok := out["lotteryShop"]; !ok {
		t.Fatalf("response missing lotteryShop: %v", out)
	}
}
