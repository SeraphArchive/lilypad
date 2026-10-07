package portal

import (
	"context"
	"errors"
	"testing"
)

func TestRegisterAutoVerifiedWhenDisabled(t *testing.T) {
	svc := NewService(NewMemoryStore(), nil, "http://localhost:8443", false)
	res, err := svc.Register(context.Background(), "User@Example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.XUID) != 32 || len(res.MigrationCode) != 16 || res.MigrationPassword == "" {
		t.Fatalf("register result incomplete: %+v", res)
	}
	if res.VerificationLink != "" {
		t.Fatal("no verification link expected when verification is off")
	}
	// login works immediately, email normalized
	cred, err := svc.Authenticate(context.Background(), "user@example.com", "password123")
	if err != nil {
		t.Fatalf("login should succeed: %v", err)
	}
	if cred.XUID != res.XUID || !cred.EmailVerified {
		t.Fatalf("auth result wrong: %+v", cred)
	}
}

func TestRegisterDuplicateEmail(t *testing.T) {
	svc := NewService(NewMemoryStore(), nil, "", false)
	_, _ = svc.Register(context.Background(), "a@b.com", "password123")
	if _, err := svc.Register(context.Background(), "a@b.com", "password123"); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("expected ErrEmailTaken, got %v", err)
	}
}

func TestVerificationFlow(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, LogMailer{}, "http://localhost:8443", true)
	res, err := svc.Register(context.Background(), "v@b.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if res.VerificationLink == "" {
		t.Fatal("verification link expected")
	}
	// login blocked until verified
	if _, err := svc.Authenticate(context.Background(), "v@b.com", "password123"); !errors.Is(err, ErrNotVerified) {
		t.Fatalf("expected ErrNotVerified, got %v", err)
	}
	// extract token from link and verify
	token := res.VerificationLink[len("http://localhost:8443/verify?token="):]
	if err := svc.VerifyEmail(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(context.Background(), "v@b.com", "password123"); err != nil {
		t.Fatalf("login should succeed after verify: %v", err)
	}
}

func TestAuthenticateWrongPassword(t *testing.T) {
	svc := NewService(NewMemoryStore(), nil, "", false)
	_, _ = svc.Register(context.Background(), "a@b.com", "password123")
	if _, err := svc.Authenticate(context.Background(), "a@b.com", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestResolveMigration(t *testing.T) {
	svc := NewService(NewMemoryStore(), nil, "", false)
	res, _ := svc.Register(context.Background(), "m@b.com", "password123")
	// The native client sends migration_password as ROT13(reverse(base64(pw))).
	xuid, err := svc.ResolveMigration(context.Background(), res.MigrationCode, clientMigrationForm(res.MigrationPassword))
	if err != nil {
		t.Fatalf("resolve should succeed: %v", err)
	}
	if xuid != res.XUID {
		t.Fatalf("xuid mismatch: %s vs %s", xuid, res.XUID)
	}
	if _, err := svc.ResolveMigration(context.Background(), res.MigrationCode, clientMigrationForm("wrongpw")); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong migration password must fail: %v", err)
	}
}

func TestResetMigrationPassword(t *testing.T) {
	svc := NewService(NewMemoryStore(), nil, "", false)
	res, err := svc.Register(context.Background(), "m@b.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveMigration(context.Background(), res.MigrationCode, clientMigrationForm(res.MigrationPassword)); err != nil {
		t.Fatalf("original password should work: %v", err)
	}
	newPw, err := svc.ResetMigrationPassword(context.Background(), "M@B.com")
	if err != nil {
		t.Fatal(err)
	}
	if newPw == "" || newPw == res.MigrationPassword {
		t.Fatalf("expected a fresh password, got %q", newPw)
	}
	if _, err := svc.ResolveMigration(context.Background(), res.MigrationCode, clientMigrationForm(res.MigrationPassword)); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password must stop working: %v", err)
	}
	xuid, err := svc.ResolveMigration(context.Background(), res.MigrationCode, clientMigrationForm(newPw))
	if err != nil {
		t.Fatalf("new password should work: %v", err)
	}
	if xuid != res.XUID {
		t.Fatalf("xuid changed after reset: %s vs %s", xuid, res.XUID)
	}
	if _, err := svc.ResetMigrationPassword(context.Background(), "missing@b.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing account: %v", err)
	}
}
