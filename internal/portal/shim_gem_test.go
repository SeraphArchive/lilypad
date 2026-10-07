package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeBalance struct{ free, paid int64 }

func (f fakeBalance) GemBalance(_ context.Context, _ string) (int64, int64, error) {
	return f.free, f.paid, nil
}

type unavailableBalance struct{}

func (unavailableBalance) GemBalance(context.Context, string) (int64, int64, error) {
	return 0, 0, errors.New("wallet unavailable")
}

type failingRequestorStore struct {
	Store
	lookupFailure bool
	bindFailure   bool
}

func (s failingRequestorStore) XUIDForRequestor(ctx context.Context, rid string) (string, error) {
	if s.lookupFailure {
		return "", errors.New("binding lookup unavailable")
	}
	return s.Store.XUIDForRequestor(ctx, rid)
}

func (s failingRequestorStore) BindRequestor(ctx context.Context, rid, xuid string) error {
	if s.bindFailure {
		return errors.New("binding write unavailable")
	}
	return s.Store.BindRequestor(ctx, rid, xuid)
}

func requireShimFailure(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["result"] != "NG" || out["entry"] != nil || out["src_x_uid"] != nil {
		t.Fatalf("failed operation returned success data: %v", out)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("sensitive platform response is cacheable")
	}
}

func TestShimBalanceFailsOnUnavailableOrInvalidStoredWallet(t *testing.T) {
	for _, provider := range []BalanceProvider{unavailableBalance{}, fakeBalance{-1, 5}, fakeBalance{math.MaxInt64, 1}} {
		sh := NewShim(nil, provider, nil)
		req := httptest.NewRequest(http.MethodGet, "/payment/balance", nil)
		req.Header.Set("Authorization", `OAuth xoauth_requestor_id="lilypad-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab"`)
		rec := httptest.NewRecorder()
		sh.Routes().ServeHTTP(rec, req)
		requireShimFailure(t, rec)
	}
}

func TestShimLookupFailureDoesNotFallbackToClientIdentity(t *testing.T) {
	svc := NewService(failingRequestorStore{Store: NewMemoryStore(), lookupFailure: true}, nil, "", false)
	sh := NewShim(svc, fakeBalance{10, 2}, nil)
	for _, path := range []string{"/payment/balance", "/auth/x_uid"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", `OAuth xoauth_requestor_id="lilypad-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab"`)
		rec := httptest.NewRecorder()
		sh.Routes().ServeHTTP(rec, req)
		requireShimFailure(t, rec)
	}
}

func TestShimMigrationRejectsMalformedOrUnpersistedTakeover(t *testing.T) {
	st := NewMemoryStore()
	svc := NewService(st, nil, "", false)
	registered, err := svc.Register(context.Background(), "migration@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	valid, err := json.Marshal(map[string]any{"migration_code": registered.MigrationCode, "migration_password": clientMigrationForm(registered.MigrationPassword)})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, requestor string
		body            []byte
		st              Store
		balance         BalanceProvider
	}{
		{"trailing JSON", "device", append(append([]byte{}, valid...), []byte(` {}`)...), st, nil},
		{"wrong field type", "device", append(append([]byte{}, valid[:len(valid)-1]...), []byte(`,"migration_code":1}`)...), st, nil},
		{"fresh device binding failure", "", valid, failingRequestorStore{Store: st, bindFailure: true}, nil},
		{"fresh device wallet failure", "", valid, st, unavailableBalance{}},
		{"fresh device invalid credentials", "", []byte(`{"migration_code":"wrong","migration_password":"wrong"}`), st, nil},
		{"failed binding", "device", valid, failingRequestorStore{Store: st, bindFailure: true}, nil},
		{"failed wallet", "device", valid, st, unavailableBalance{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/migration/code/verify", bytes.NewReader(tc.body))
			if tc.requestor != "" {
				req.Header.Set("Authorization", `OAuth xoauth_requestor_id="`+tc.requestor+`"`)
			}
			rec := httptest.NewRecorder()
			NewShim(NewService(tc.st, nil, "", false), tc.balance, nil).Routes().ServeHTTP(rec, req)
			requireShimFailure(t, rec)
			if _, err := st.XUIDForRequestor(context.Background(), "device"); !errors.Is(err, ErrNotFound) {
				t.Fatal("failed takeover changed durable binding")
			}
			if _, err := st.XUIDForRequestor(context.Background(), "lilypad-"+registered.XUID); !errors.Is(err, ErrNotFound) {
				t.Fatal("failed fresh takeover created a source UUID binding")
			}
		})
	}
}

func balanceFields(t *testing.T, body *httptest.ResponseRecorder) (result, free, charge, total string) {
	t.Helper()
	var resp struct {
		Result string `json:"result"`
		Entry  struct {
			Free   string `json:"balance_free_gem"`
			Charge string `json:"balance_charge_gem"`
			Total  string `json:"balance_total_gem"`
		} `json:"entry"`
	}
	if err := json.NewDecoder(body.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.Result, resp.Entry.Free, resp.Entry.Charge, resp.Entry.Total
}

func TestShimBalanceReportsStored(t *testing.T) {
	sh := NewShim(nil, fakeBalance{free: 1200, paid: 30}, nil)
	req := httptest.NewRequest(http.MethodGet, "/payment/balance", nil)
	req.Header.Set("Authorization", `OAuth xoauth_requestor_id="lilypad-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab"`)
	rec := httptest.NewRecorder()
	sh.balance(rec, req)
	result, free, charge, total := balanceFields(t, rec)
	if result != "OK" || free != "1200" || charge != "30" || total != "1230" {
		t.Fatalf("balance wrong: result=%s free=%s charge=%s total=%s", result, free, charge, total)
	}
}

// A lilypad- requestor only resolves when its suffix is a 32-hex platform
// XUID. Anything else is "no binding", not a fresh identity.
func TestXuidFromRequestRejectsNonXUIDSuffix(t *testing.T) {
	sh := NewShim(nil, nil, nil)
	for _, rid := range []string{"lilypad-ABC123", "lilypad-1000001", "lilypad-", "lilypad-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaB"} {
		req := httptest.NewRequest(http.MethodGet, "/payment/balance", nil)
		req.Header.Set("Authorization", `OAuth xoauth_requestor_id="`+rid+`"`)
		if got, err := sh.xuidFromRequest(req); got != "" || err != nil {
			t.Fatalf("%s resolved to %q, want nothing", rid, got)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/payment/balance", nil)
	req.Header.Set("Authorization", `OAuth xoauth_requestor_id="lilypad-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab"`)
	if got, err := sh.xuidFromRequest(req); got != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab" || err != nil {
		t.Fatalf("valid suffix resolved to %q", got)
	}
}

func TestShimBalanceNilProvider(t *testing.T) {
	sh := NewShim(nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/payment/balance", nil)
	req.Header.Set("Authorization", `OAuth xoauth_requestor_id="lilypad-X"`)
	rec := httptest.NewRecorder()
	sh.balance(rec, req)
	result, free, charge, total := balanceFields(t, rec)
	if result != "OK" || free != "0" || charge != "0" || total != "0" {
		t.Fatalf("nil provider should be 0/0/0: result=%s free=%s charge=%s total=%s", result, free, charge, total)
	}
}

func TestShimRequestorBindingSurvivesNewShim(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, nil, "", false)
	res, err := svc.Register(context.Background(), "restart@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	verifyBody := []byte(`{"migration_code":"` + res.MigrationCode + `","migration_password":"` + clientMigrationForm(res.MigrationPassword) + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/migration/code/verify", bytes.NewReader(verifyBody))
	req.Header.Set("Authorization", `OAuth xoauth_requestor_id="requestor-fixed"`)
	rec := httptest.NewRecorder()
	NewShim(svc, nil, nil).migrationVerify(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify status=%d", rec.Code)
	}

	restarted := NewShim(NewService(store, nil, "", false), nil, nil)
	xreq := httptest.NewRequest(http.MethodGet, "/auth/x_uid", nil)
	xreq.Header.Set("Authorization", `OAuth xoauth_requestor_id="requestor-fixed", oauth_consumer_key="app"`)
	xrec := httptest.NewRecorder()
	restarted.authXuid(xrec, xreq)
	var out struct {
		Result string `json:"result"`
		XUID   string `json:"x_uid"`
	}
	if err := json.NewDecoder(xrec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Result != "OK" || out.XUID != res.XUID {
		t.Fatalf("auth/x_uid after restart = %+v, want xuid %s", out, res.XUID)
	}
}

func TestShimAuthNowReturnsUnixNumber(t *testing.T) {
	sh := NewShim(nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/auth/now", nil)
	rec := httptest.NewRecorder()
	sh.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("auth/now status=%d", rec.Code)
	}
	var out struct {
		Result string      `json:"result"`
		T      json.Number `json:"t"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Result != "OK" {
		t.Fatalf("result=%s", out.Result)
	}
	n, err := out.T.Int64()
	if err != nil || n <= 0 {
		t.Fatalf("t must be a positive unix number, got %q err=%v", out.T, err)
	}
}

func TestShimLinkedActiveRoutes(t *testing.T) {
	sh := NewShim(nil, nil, nil)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/linked/active/info"},
		{http.MethodPost, "/linked/active/update"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("X-GREE-GAMELIB", "model=Windows&appLanguage=tchinese")
		rec := httptest.NewRecorder()
		sh.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s status=%d", tc.method, tc.path, rec.Code)
		}
		var out struct {
			Result     string `json:"result"`
			Model      string `json:"model"`
			ActiveTime string `json:"activeTime"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if out.Result != "OK" || out.Model != "Windows" || out.ActiveTime == "" {
			t.Fatalf("%s body=%+v", tc.path, out)
		}
	}
}

func TestShimProductListHasWelcome(t *testing.T) {
	sh := NewShim(nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/payment/productlist", nil)
	rec := httptest.NewRecorder()
	sh.Routes().ServeHTTP(rec, req)
	var out struct {
		Result string `json:"result"`
		Entry  struct {
			Products []any  `json:"products"`
			Welcome  string `json:"welcome"`
		} `json:"entry"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Result != "OK" || out.Entry.Welcome != "0" || len(out.Entry.Products) < 7 {
		t.Fatalf("productlist = %+v", out)
	}
}

func TestShimAuthXuidPostMatchesGet(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, nil, "", false)
	res, err := svc.Register(context.Background(), "postxuid@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.BindRequestor(context.Background(), "requestor-post", res.XUID); err != nil {
		t.Fatal(err)
	}
	sh := NewShim(svc, nil, nil)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req := httptest.NewRequest(method, "/auth/x_uid", nil)
		req.Header.Set("Authorization", `OAuth xoauth_requestor_id="requestor-post"`)
		rec := httptest.NewRecorder()
		sh.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s /auth/x_uid status=%d (registerXuid must not 405)", method, rec.Code)
		}
		var out struct {
			Result string `json:"result"`
			XUID   string `json:"x_uid"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if out.Result != "OK" || out.XUID != res.XUID {
			t.Fatalf("%s auth/x_uid = %+v, want %s", method, out, res.XUID)
		}
	}
}
