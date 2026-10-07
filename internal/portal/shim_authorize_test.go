package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func platformCall(t *testing.T, sh *Shim, method, path, requestor string, body any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	if requestor != "" {
		req.Header.Set("Authorization", `OAuth xoauth_requestor_id="`+requestor+`"`)
	}
	rec := httptest.NewRecorder()
	sh.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: HTTP %d", path, rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func freshPlatformRequest(device, key, ticket string) map[string]string {
	return map[string]string{"device_id": device, "token": key, "id_token": ticket}
}

func TestFreshPlatformAuthorizeThenMigration(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, nil, "", false)
	account, err := svc.Register(context.Background(), "fresh-platform@example.invalid", "fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	sh := NewShim(svc, nil, nil)
	request := freshPlatformRequest("device-one", "public-key-one", "steam-ticket-one")
	auth := platformCall(t, sh, "POST", "/auth/authorize", "", request)
	uuid, _ := auth["uuid"].(string)
	if auth["result"] != "OK" || uuid == "" {
		t.Fatalf("SDK cannot persist fresh auth identity: %v", auth)
	}
	// Authorizing a device must not create/select a player account. Only a
	// successfully verified takeover credential can establish that binding.
	if out := platformCall(t, sh, "GET", "/auth/x_uid", uuid, nil); out["result"] == "OK" {
		t.Fatal("authorization bypassed migration")
	}
	migration := platformCall(t, sh, "POST", "/migration/code/verify", uuid, map[string]string{
		"migration_code": account.MigrationCode, "migration_password": clientMigrationForm(account.MigrationPassword),
	})
	if migration["result"] != "OK" {
		t.Fatalf("fresh migration failed: %v", migration)
	}
	for _, requestor := range []string{uuid, migration["src_uuid"].(string)} {
		// A new server instance must preserve both the pre-transfer requestor
		// binding and the UUID adopted by the SDK after transfer.
		restarted := NewShim(NewService(store, nil, "", false), nil, nil)
		out := platformCall(t, restarted, "GET", "/auth/x_uid", requestor, nil)
		if out["result"] != "OK" || out["x_uid"] != account.XUID {
			t.Fatalf("identity not preserved: %v", out)
		}
		reauth := platformCall(t, restarted, "POST", "/auth/authorize", requestor, map[string]string{"id_token": "new-ticket"})
		if reauth["uuid"] != requestor {
			t.Fatal("reauthorization rotated an established requestor")
		}
	}
}

func TestPlatformReauthorizeRequiresTicket(t *testing.T) {
	for _, ticket := range []string{"", " "} {
		out := platformCall(t, NewShim(nil, nil, nil), "POST", "/auth/authorize", "existing-requestor", map[string]string{"id_token": ticket})
		if out["result"] == "OK" {
			t.Fatal("accepted empty reauthorization ticket")
		}
	}
	out := platformCall(t, NewShim(nil, nil, nil), "POST", "/auth/authorize", "", map[string]string{"id_token": "ticket"})
	if out["result"] == "OK" {
		t.Fatal("ticket-only request created an identity")
	}
}

func TestPlatformAuthorizeRetriesAndProfileSeparation(t *testing.T) {
	request := freshPlatformRequest("same-device", "key-a", "ticket-a")
	first := platformCall(t, NewShim(nil, nil, nil), "POST", "/auth/authorize", "", request)
	uuid, _ := first["uuid"].(string)
	if uuid == "" {
		t.Fatal("missing device UUID")
	}
	request["id_token"] = "refreshed-ticket"
	retry := platformCall(t, NewShim(nil, nil, nil), "POST", "/auth/authorize", "", request)
	if retry["uuid"] != uuid {
		t.Fatal("retry or restart changed device identity")
	}
	for _, other := range []map[string]string{
		freshPlatformRequest("same-device", "key-b", "ticket-a"),
		freshPlatformRequest("other-device", "key-a", "ticket-a"),
		freshPlatformRequest("same", "-devicekey-a", "ticket-a"),
	} {
		if platformCall(t, NewShim(nil, nil, nil), "POST", "/auth/authorize", "", other)["uuid"] == uuid {
			t.Fatal("distinct installations collided")
		}
	}
	if suffix, ok := strings.CutPrefix(uuid, "lilypad-"); ok && isPlatformXUID(suffix) {
		t.Fatal("unbound device UUID can be interpreted as a player XUID")
	}
}

func TestPlatformAuthorizeRejectsIncompleteIdentity(t *testing.T) {
	for _, missing := range []string{"device_id", "token", "id_token"} {
		body := freshPlatformRequest("device", "key", "ticket")
		delete(body, missing)
		out := platformCall(t, NewShim(nil, nil, nil), "POST", "/auth/authorize", "", body)
		if out["result"] == "OK" || out["uuid"] != nil {
			t.Fatalf("accepted missing %s", missing)
		}
	}
}
