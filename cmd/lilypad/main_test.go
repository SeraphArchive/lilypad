package main

import (
	"bytes"
	"io"
	"lilypad/internal/config"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccessLogDoesNotConsumeOrExposeCredentials(t *testing.T) {
	var log bytes.Buffer
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil || string(raw) != "body-secret" {
			t.Fatal("logger consumed body")
		}
		w.WriteHeader(200)
	})
	req := httptest.NewRequest("POST", "/v1.0/migration/code/verify?token=query-secret&migration_code=takeover-secret", strings.NewReader("body-secret"))
	req.Header.Set("Authorization", "auth-secret")
	accessLog(handler, slog.New(slog.NewTextHandler(&log, nil))).ServeHTTP(httptest.NewRecorder(), req)
	for _, secret := range []string{"body-secret", "query-secret", "auth-secret", "takeover-secret"} {
		if strings.Contains(log.String(), secret) {
			t.Fatalf("secret logged: %s", secret)
		}
	}
}
func TestInvalidConfiguredMasterFailsAndReadyDispatches(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := loadMaster(&config.Config{DataDir: t.TempDir()}, log); err == nil {
		t.Fatal("empty configured data accepted")
	}
	called := false
	game := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	dispatch(game, http.NotFoundHandler(), http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/readyz", nil))
	if !called {
		t.Fatal("readiness routed to Portal")
	}
}
