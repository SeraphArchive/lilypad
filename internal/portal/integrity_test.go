package portal

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type failingMailer struct{}

type successfulMailer struct{}

func (successfulMailer) SendVerification(context.Context, string, string) error { return nil }

func (failingMailer) SendVerification(context.Context, string, string) error {
	return errors.New("mail unavailable")
}

func TestMemoryVerificationTokenConsumedOnceAndResendSerialized(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore()
	rec := CredentialRecord{Email: "concurrent@example.com", VerificationToken: "old-token", VerificationSentAt: time.Now().Add(-time.Hour)}
	if err := st.CreateCredential(ctx, rec); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for range 12 {
		wg.Go(func() { results <- st.UpdateVerification(ctx, rec.Email, GenerateToken()) })
	}
	wg.Wait()
	close(results)
	count := 0
	for err := range results {
		if err == nil {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("concurrent resends admitted %d replacements", count)
	}
	if err := st.ConsumeVerificationToken(ctx, rec.VerificationToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rotated token accepted: %v", err)
	}
	current, _ := st.GetCredentialByEmail(ctx, rec.Email)
	results = make(chan error, 12)
	for range 12 {
		wg.Go(func() { results <- st.ConsumeVerificationToken(ctx, current.VerificationToken) })
	}
	wg.Wait()
	close(results)
	count = 0
	for err := range results {
		if err == nil {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("verification token consumed %d times", count)
	}
}
func TestVerificationRequiredCannotCreateAuthenticatedSession(t *testing.T) {
	svc := NewService(NewMemoryStore(), failingMailer{}, "http://localhost", true)
	web, err := NewWeb(svc, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"email": {"verify@example.com"}, "password": {"password123"}}
	req := httptest.NewRequest("POST", "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	web.Routes().ServeHTTP(rec, req)
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("unverified session cookie issued")
	}
	if !strings.Contains(rec.Body.String(), "Account created") {
		t.Fatal("mail failure lost successful registration")
	}
	req = httptest.NewRequest("GET", "/account", nil)
	rec = httptest.NewRecorder()
	web.Routes().ServeHTTP(rec, req)
	if rec.Code != 303 {
		t.Fatal(rec.Code)
	}
	store := svc.store.(*MemoryStore)
	store.mu.Lock()
	account := store.byEmail["verify@example.com"]
	account.VerificationSentAt = time.Now().Add(-time.Hour)
	store.byEmail[account.Email] = account
	store.mu.Unlock()
	svc.mailer = successfulMailer{}
	if err := svc.ResendVerification(context.Background(), account.Email, "password123"); err != nil {
		t.Fatal(err)
	}
	updated, _ := store.GetCredentialByEmail(context.Background(), account.Email)
	if updated.VerificationToken == account.VerificationToken {
		t.Fatal("resend did not replace token")
	}
	if err := svc.VerifyEmail(context.Background(), updated.VerificationToken); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(context.Background(), account.Email, "password123"); err != nil {
		t.Fatal(err)
	}
}
func TestMalformedArchiveAndWalletRejected(t *testing.T) {
	if _, _, err := parseArchive([]byte(`{"format":"lilypad-export/1","tables":{"user_item":[{"_num":1}]}}`)); err == nil {
		t.Fatal("missing collection key accepted")
	}
	for _, pb := range []map[string]any{{"balance_free_gem": "-5", "balance_charge_gem": "10"}, {"balance_free_gem": "5"}, {"balance_free_gem": "9223372036854775807", "balance_charge_gem": "1"}} {
		if _, _, ok := paymentBalanceFrom(map[string]any{"paymentBalance": pb}); ok {
			t.Fatal(pb)
		}
	}
	web, _ := newTestWeb(t)
	rec := httptest.NewRecorder()
	web.render(rec, httptest.NewRequest("GET", "/transfer", nil), "transfer", viewData{Error: "save unavailable"})
	if !strings.Contains(rec.Body.String(), "<h2>Import</h2>") {
		t.Fatal("error page was truncated")
	}
}

func TestExpiredSessionsPrunedWithoutRevisitingCookie(t *testing.T) {
	web, _ := newTestWeb(t)
	web.sessions.m["abandoned"] = session{email: "old@example.com", expires: time.Now().Add(-time.Hour)}
	web.startSession(httptest.NewRecorder(), "new@example.com")
	if _, exists := web.sessions.m["abandoned"]; exists {
		t.Fatal("expired abandoned session retained")
	}
}
