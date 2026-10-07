package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"lilypad/internal/portal"
)

func TestPGCredentialLifecycle(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, dsnOrSkip(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	email := fmt.Sprintf("cred-%d@example.com", time.Now().UnixNano())
	rec := portal.CredentialRecord{
		Email:                 email,
		PasswordHash:          "pwhash",
		EmailVerified:         false,
		VerificationToken:     "vtok-" + email,
		MigrationCode:         "MIGCODE" + fmt.Sprint(time.Now().UnixNano()%100000),
		MigrationPasswordHash: "mighash",
		XUID:                  "xuid-" + email,
	}
	if err := s.CreateCredential(ctx, rec); err != nil {
		t.Fatal(err)
	}
	// duplicate email rejected
	if err := s.CreateCredential(ctx, rec); !errors.Is(err, portal.ErrEmailTaken) {
		t.Fatalf("expected ErrEmailTaken, got %v", err)
	}
	got, err := s.GetCredentialByEmail(ctx, email)
	if err != nil || got.XUID != rec.XUID || got.MigrationCode != rec.MigrationCode {
		t.Fatalf("get mismatch: %+v err=%v", got, err)
	}
	// find by token, verify, token cleared
	byTok, err := s.FindByVerificationToken(ctx, rec.VerificationToken)
	if err != nil || byTok.Email != email {
		t.Fatalf("find by token: %+v err=%v", byTok, err)
	}
	if err := s.ConsumeVerificationToken(ctx, rec.VerificationToken); err != nil {
		t.Fatal(err)
	}
	after, _ := s.GetCredentialByEmail(ctx, email)
	if !after.EmailVerified || after.VerificationToken != "" {
		t.Fatalf("verify did not stick: %+v", after)
	}
	// find by migration code
	byCode, err := s.FindByMigrationCode(ctx, rec.MigrationCode)
	if err != nil || byCode.XUID != rec.XUID {
		t.Fatalf("find by migration code: %+v err=%v", byCode, err)
	}
	// reset migration password hash
	if err := s.UpdateMigrationPasswordHash(ctx, email, "new-mig-hash"); err != nil {
		t.Fatal(err)
	}
	afterReset, err := s.GetCredentialByEmail(ctx, email)
	if err != nil || afterReset.MigrationPasswordHash != "new-mig-hash" {
		t.Fatalf("hash not updated: %+v err=%v", afterReset, err)
	}
	if err := s.UpdateMigrationPasswordHash(ctx, "nope@nope.com", "x"); !errors.Is(err, portal.ErrNotFound) {
		t.Fatalf("reset missing email: %v", err)
	}
	// not found
	if _, err := s.GetCredentialByEmail(ctx, "nope@nope.com"); !errors.Is(err, portal.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPGVerificationTokenConsumedOnceAndResendSerialized(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, dsnOrSkip(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	email := fmt.Sprintf("verification-%d@example.com", time.Now().UnixNano())
	rec := portal.CredentialRecord{Email: email, PasswordHash: "test", VerificationToken: "old-token-" + email,
		MigrationCode: portal.GenerateMigrationCode(), MigrationPasswordHash: "test", XUID: portal.GenerateXUID()}
	if err := s.CreateCredential(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE credentials SET verification_sent_at=now()-INTERVAL '2 minutes' WHERE email=$1`, email); err != nil {
		t.Fatal(err)
	}
	runConcurrently := func(action func(int) error) int {
		t.Helper()
		var wg sync.WaitGroup
		results := make(chan error, 12)
		for i := range 12 {
			wg.Go(func() { results <- action(i) })
		}
		wg.Wait()
		close(results)
		succeeded := 0
		for err := range results {
			if err == nil {
				succeeded++
			} else if !errors.Is(err, portal.ErrNotFound) {
				t.Fatalf("unexpected verification error: %v", err)
			}
		}
		return succeeded
	}
	if count := runConcurrently(func(i int) error { return s.UpdateVerification(ctx, email, fmt.Sprintf("replacement-%d-%s", i, email)) }); count != 1 {
		t.Fatalf("concurrent resends admitted %d replacements", count)
	}
	if err := s.ConsumeVerificationToken(ctx, rec.VerificationToken); !errors.Is(err, portal.ErrNotFound) {
		t.Fatalf("replaced token accepted: %v", err)
	}
	current, err := s.GetCredentialByEmail(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	if count := runConcurrently(func(int) error { return s.ConsumeVerificationToken(ctx, current.VerificationToken) }); count != 1 {
		t.Fatalf("verification token consumed %d times", count)
	}
	if err := s.UpdateVerification(ctx, email, "cannot-reissue"); !errors.Is(err, portal.ErrNotFound) {
		t.Fatalf("verified account received a new token: %v", err)
	}
}

func TestPGVerificationRejectsExpiredMissingAndFutureIssueTimes(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, dsnOrSkip(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	for _, issued := range []*time.Time{nil, timePointer(time.Now().Add(-25 * time.Hour)), timePointer(time.Now().Add(time.Hour))} {
		email := fmt.Sprintf("expiry-%d@example.com", time.Now().UnixNano())
		rec := portal.CredentialRecord{Email: email, PasswordHash: "test", VerificationToken: "expiry-token-" + email,
			MigrationCode: portal.GenerateMigrationCode(), MigrationPasswordHash: "test", XUID: portal.GenerateXUID()}
		if err := s.CreateCredential(ctx, rec); err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `UPDATE credentials SET verification_sent_at=$2 WHERE email=$1`, email, issued); err != nil {
			t.Fatal(err)
		}
		if err := s.ConsumeVerificationToken(ctx, rec.VerificationToken); !errors.Is(err, portal.ErrNotFound) {
			t.Fatalf("invalid issue time %v accepted: %v", issued, err)
		}
		current, err := s.GetCredentialByEmail(ctx, email)
		if err != nil || current.EmailVerified {
			t.Fatalf("rejected token modified credential: %+v %v", current, err)
		}
	}
}

func timePointer(t time.Time) *time.Time { return &t }
