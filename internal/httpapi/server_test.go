package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"lilypad/internal/account"
	"lilypad/internal/clientversion"
	"lilypad/internal/codec"
	"lilypad/internal/config"
	"lilypad/internal/sign"
)

func newTestServer() *Server {
	cfg := &config.Config{}
	cfg.Asset.Version = "6.5.15"
	cfg.Asset.Hash = "deadbeef"
	return New(cfg, sign.NoopSigner{}, account.NewMemory(0), nil, nil, nil)
}

func do(t *testing.T, s *Server, route string, reqObj any) map[string]any {
	t.Helper()
	wire, err := codec.Encode(reqObj)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, route, bytes.NewReader(wire))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%q", route, rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("content-type=%q", ct)
	}
	out, err := codec.Decode(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

func TestAppStart(t *testing.T) {
	s := newTestServer()
	out := do(t, s, "/api/app/start", map[string]any{
		"hdr": "", "region": "2", "country": "JP", "language": "ja",
	})
	if out["assetVersion"] != "6.5.15" || out["assetHash"] != "deadbeef" {
		t.Fatalf("asset fields wrong: %v", out)
	}
	if _, ok := out["serverCommand"]; !ok {
		t.Fatal("missing serverCommand")
	}
	if _, ok := out["lotteryShop"]; !ok {
		t.Fatal("missing lotteryShop")
	}
}

func TestClientIDCreatesStableUserID(t *testing.T) {
	s := newTestServer()
	out1 := do(t, s, "/api/user/client/id", map[string]any{"hdr": "", "x_uid": "abc"})
	out2 := do(t, s, "/api/user/client/id", map[string]any{"hdr": "", "x_uid": "abc"})
	if out1["userId"].(interface{ String() string }).String() != "1" {
		t.Fatalf("first userId not 1: %v", out1["userId"])
	}
	if out1["userId"].(interface{ String() string }).String() != out2["userId"].(interface{ String() string }).String() {
		t.Fatalf("userId not stable: %v vs %v", out1["userId"], out2["userId"])
	}
	if _, ok := out1["code"]; ok {
		t.Fatal("client/id must NOT carry a code field (matches capture)")
	}
}

func TestClientIDMissingXUIDFails(t *testing.T) {
	s := newTestServer()
	out := do(t, s, "/api/user/client/id", map[string]any{"hdr": ""})
	if out["code"] == nil {
		t.Fatalf("expected failure code, got %v", out)
	}
}

func TestSignerSetsHeader(t *testing.T) {
	cfg := &config.Config{}
	priv, _, err := sign.GenerateKeyPEM(1024)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := sign.NewRSASigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	s := New(cfg, signer, account.NewMemory(0), nil, nil, nil)
	wire, _ := codec.Encode(map[string]any{"hdr": "", "x_uid": "z"})
	req := httptest.NewRequest(http.MethodPost, "/api/user/client/id", bytes.NewReader(wire))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Header().Get("x-signature") == "" {
		t.Fatal("rsa signer must set x-signature")
	}
}

func TestHealthz(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("healthz: %d %q", rec.Code, rec.Body.String())
	}
}

// postReport drives the plain-JSON report endpoint (not the codec).
func postReport(t *testing.T, s *Server, body string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/clientpatch/version", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("report status=%d body=%q", rec.Code, rec.Body.String())
	}
}

// appStartWith returns the decoded app/start response for a given client id.
func appStartWith(t *testing.T, s *Server, clientID string) map[string]any {
	t.Helper()
	wire, err := codec.Encode(map[string]any{"hdr": "", "region": "2", "country": "JP", "language": "ja"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/app/start", bytes.NewReader(wire))
	if clientID != "" {
		req.Header.Set("x-clientpatch-id", clientID)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	out, err := codec.Decode(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

func TestClientVersionReportOverridesAppStart(t *testing.T) {
	s := newTestServer()
	postReport(t, s, `{"clientId":"cp-1","programVersion":"6.9.0","assetVersion":"6.9.0","assetHash":"sha1"}`)
	out := appStartWith(t, s, "cp-1")
	if out["assetVersion"] != "6.9.0" || out["assetHash"] != "sha1" {
		t.Fatalf("asset fields not overridden: %v", out)
	}
}

func TestAppStartGlobalFallbackForUnknownClient(t *testing.T) {
	s := newTestServer()
	s.versions.Report("cp-1", clientversion.Report{AssetVersion: "9.9.9", AssetHash: "zzz"})
	out := appStartWith(t, s, "unknown-id")
	if out["assetVersion"] != "9.9.9" || out["assetHash"] != "zzz" {
		t.Fatalf("global fallback not applied: %v", out)
	}
}

func TestAppStartConfigFallbackWithoutAnyReport(t *testing.T) {
	s := newTestServer()
	out := appStartWith(t, s, "unknown-id")
	if out["assetVersion"] != "6.5.15" || out["assetHash"] != "deadbeef" {
		t.Fatalf("config fallback not applied: %v", out)
	}
}

func TestClientVersionReportRejectsMissingFields(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest(http.MethodPost, "/api/clientpatch/version", bytes.NewBufferString(`{"clientId":"cp-1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
