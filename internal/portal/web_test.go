package portal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func newTestWeb(t *testing.T) (*Web, http.Handler) {
	t.Helper()
	svc := NewService(NewMemoryStore(), LogMailer{}, "http://localhost", false)
	web, err := NewWeb(svc, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	return web, web.Routes()
}

func TestRegisterFlowRendersSuccessAndSession(t *testing.T) {
	_, h := newTestWeb(t)
	form := url.Values{"email": {"a@b.com"}, "password": {"password123"}}
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("register status=%d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Account created") || !strings.Contains(body, "Takeover code") {
		t.Fatalf("success page missing expected content")
	}
	if !strings.Contains(body, "sidebar") {
		t.Fatal("left-sidebar layout not rendered")
	}
	// a session cookie should be set
	if len(rec.Result().Cookies()) == 0 {
		t.Fatal("expected session cookie after register")
	}
}

func TestLoginRequiredRedirect(t *testing.T) {
	_, h := newTestWeb(t)
	req := httptest.NewRequest(http.MethodGet, "/account", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("unauth /account should redirect, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Fatalf("redirect to %q, want /login", loc)
	}
}

func TestLoginThenAccount(t *testing.T) {
	web, h := newTestWeb(t)
	// seed an account directly via the service
	if _, err := web.svc.Register(context.Background(), "u@b.com", "password123"); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"email": {"u@b.com"}, "password": {"password123"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login status=%d", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("no session cookie after login")
	}
	// follow to /account with the cookie
	req2 := httptest.NewRequest(http.MethodGet, "/account", nil)
	req2.AddCookie(cookies[0])
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), "u@b.com") {
		t.Fatalf("account page missing email: %d", rec2.Code)
	}
}

func TestPortalRejectsCrossOriginAuthentication(t *testing.T) {
	web, h := newTestWeb(t)
	if _, err := web.svc.Register(context.Background(), "attacker@example.com", "password123"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/login", "/register", "/verify/resend"} {
		for _, header := range []string{"Origin", "Sec-Fetch-Site"} {
			form := url.Values{"email": {"attacker@example.com"}, "password": {"password123"}}
			req := httptest.NewRequest(http.MethodPost, "http://localhost"+path, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if header == "Origin" {
				req.Header.Set(header, "https://attacker.example")
			} else {
				req.Header.Set(header, "cross-site")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || findSessionCookie(rec) != nil {
				t.Fatalf("%s %s: cross-origin authentication accepted: %d", path, header, rec.Code)
			}
		}
	}
	form := url.Values{"email": {"attacker@example.com"}, "password": {"password123"}}
	req := httptest.NewRequest(http.MethodPost, "http://localhost/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://localhost")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || findSessionCookie(rec) == nil {
		t.Fatalf("same-origin login failed: %d", rec.Code)
	}
}

func TestStaticCSSServed(t *testing.T) {
	_, h := newTestWeb(t)
	req := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), ".sidebar") {
		t.Fatalf("static css not served: %d", rec.Code)
	}
}

func findSessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	return nil
}

func extractCSRF(body string) string {
	const marker = `name="csrf" value="`
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	rest := body[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func loginCookie(t *testing.T, h http.Handler, email, password string) *http.Cookie {
	t.Helper()
	form := url.Values{"email": {email}, "password": {password}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	c := findSessionCookie(rec)
	if c == nil {
		t.Fatalf("login status=%d, no session cookie", rec.Code)
	}
	return c
}

func TestTakeoverResetRequiresAuth(t *testing.T) {
	_, h := newTestWeb(t)
	req := httptest.NewRequest(http.MethodPost, "/takeover/reset", strings.NewReader("csrf=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("unauth reset should redirect, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Fatalf("redirect to %q, want /login", loc)
	}
}

func TestTakeoverResetShowsPasswordOnce(t *testing.T) {
	web, h := newTestWeb(t)
	res, err := web.svc.Register(context.Background(), "u@b.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	cookie := loginCookie(t, h, "u@b.com", "password123")

	req := httptest.NewRequest(http.MethodGet, "/takeover", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /takeover status=%d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, res.MigrationCode) {
		t.Fatal("takeover page missing migration code")
	}
	if strings.Contains(body, "Takeover password") {
		t.Fatal("GET /takeover must not reveal a password")
	}
	csrf := extractCSRF(body)
	if csrf == "" {
		t.Fatal("missing csrf on takeover page")
	}

	form := url.Values{"csrf": {csrf}}
	req2 := httptest.NewRequest(http.MethodPost, "/takeover/reset", strings.NewReader(form.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.AddCookie(cookie)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	body2 := rec2.Body.String()
	if !strings.Contains(body2, "Takeover password") || !strings.Contains(body2, "Save it now") {
		t.Fatalf("reset page should show the new password once: %s", body2)
	}
	if strings.Contains(body2, res.MigrationPassword) {
		t.Fatal("reset page showed the old password")
	}

	req3 := httptest.NewRequest(http.MethodGet, "/takeover", nil)
	req3.AddCookie(cookie)
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)
	if strings.Contains(rec3.Body.String(), "Takeover password") {
		t.Fatal("subsequent GET must not show the reset password")
	}
}

func TestTakeoverResetRejectsBadCSRF(t *testing.T) {
	web, h := newTestWeb(t)
	if _, err := web.svc.Register(context.Background(), "u@b.com", "password123"); err != nil {
		t.Fatal(err)
	}
	cookie := loginCookie(t, h, "u@b.com", "password123")
	form := url.Values{"csrf": {"not-the-token"}}
	req := httptest.NewRequest(http.MethodPost, "/takeover/reset", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bad csrf status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid session") {
		t.Fatalf("expected csrf error, got %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Takeover password") {
		t.Fatal("bad csrf must not mint a password")
	}
}
