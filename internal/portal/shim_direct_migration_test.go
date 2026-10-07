package portal

import (
	"context"
	"testing"
)

func TestMigrationBeforeAuthorization(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, nil, "", false)
	account, err := svc.Register(context.Background(), "direct-migration@example.invalid", "fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	sh := NewShim(svc, nil, nil)
	// The title-screen native path does not call /auth/authorize first and has
	// neither xoauth_requestor_id nor a destination UUID in fresh auth storage.
	verified := platformCall(t, sh, "POST", "/migration/code/verify", "", map[string]string{
		"migration_code":     account.MigrationCode,
		"migration_password": clientMigrationForm(account.MigrationPassword),
	})
	if verified["result"] != "OK" {
		t.Fatalf("direct title-screen migration rejected: %v", verified)
	}
	source, _ := verified["src_uuid"].(string)
	token, _ := verified["migration_token"].(string)
	if source == "" || token == "" || verified["src_x_uid"] != account.XUID {
		t.Fatal("incomplete takeover response")
	}
	bound, err := svc.XUIDForRequestor(context.Background(), source)
	if err != nil || bound != account.XUID {
		t.Fatalf("returned UUID was not durably bound: %v", err)
	}
	completed := platformCall(t, sh, "POST", "/migration", "", map[string]string{
		"src_uuid": source, "migration_token": token,
		"device_id": "fresh-device", "token": "fresh-public-key",
	})
	if completed["result"] != "OK" {
		t.Fatal("migration without optional dst_uuid failed")
	}
	// The SDK adopts src_uuid only after the successful /migration response.
	restarted := NewShim(NewService(store, nil, "", false), nil, nil)
	queried := platformCall(t, restarted, "GET", "/auth/x_uid", source, nil)
	if queried["result"] != "OK" || queried["x_uid"] != account.XUID {
		t.Fatalf("post-migration identity failed: %v", queried)
	}
}
