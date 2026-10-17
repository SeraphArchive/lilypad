package portal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"lilypad/internal/gem"
)

type historyBalance struct {
	fakeBalance
	entries              []gem.Transaction
	err                  error
	xuid                 string
	offset, limit, calls int
}

func (f *historyBalance) GemHistory(_ context.Context, xuid string, offset, limit int) ([]gem.Transaction, bool, error) {
	f.xuid, f.offset, f.limit = xuid, offset, limit
	f.calls++
	if f.err != nil {
		return nil, false, f.err
	}
	if offset >= len(f.entries) {
		return []gem.Transaction{}, false, nil
	}
	end := min(offset+limit, len(f.entries))
	return f.entries[offset:end], end < len(f.entries), nil
}

func historyRequest(sh *Shim, path, requestor string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if requestor != "" {
		req.Header.Set("Authorization", `OAuth xoauth_requestor_id="`+requestor+`"`)
	}
	rec := httptest.NewRecorder()
	sh.Routes().ServeHTTP(rec, req)
	return rec
}

func historyResponse(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || out["result"] != "OK" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("history response", rec.Code, out, rec.Header())
	}
	entry, ok := out["entry"].(map[string]any)
	if !ok {
		t.Fatal("missing history entry", out)
	}
	if _, ok := entry["offset"].(string); !ok {
		t.Fatal("offset is not a string", entry)
	}
	if _, ok := entry["limit"].(string); !ok {
		t.Fatal("limit is not a string", entry)
	}
	if _, ok := entry["has_next"].(bool); !ok {
		t.Fatal("has_next is not a boolean", entry)
	}
	if _, ok := entry["transactions"].([]any); !ok {
		t.Fatal("transactions is not a non-null array", entry)
	}
	return entry
}

func TestShimQuartzHistoryNativeWireTypes(t *testing.T) {
	date := time.Date(2026, 10, 6, 19, 6, 23, 0, time.UTC)
	provider := &historyBalance{entries: []gem.Transaction{
		{Type: gem.TransactionConsume, Date: date, Free: -200, Paid: -100, Reason: gem.Reason{Type: gem.ReasonLottery, MasterLotteryID: 100}},
		{Type: gem.TransactionIssue, Date: date, Free: 20, Reason: gem.Reason{Type: gem.ReasonServerGift, MasterReasonID: 900}},
		{Type: gem.TransactionIssue, Date: date, Free: 50, Reason: gem.Reason{Type: gem.ReasonMission, MasterMissionID: 500}},
		{Type: gem.TransactionIssue, Date: date, Free: 20, Reason: gem.Reason{Type: gem.ReasonClientGift}},
	}}
	entry := historyResponse(t, historyRequest(NewShim(nil, provider, nil), "/payment/history", "lilypad-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab"))
	if entry["offset"] != "0" || entry["limit"] != "20" || entry["has_next"] != false || provider.xuid != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab" {
		t.Fatal(entry, provider)
	}
	rows := entry["transactions"].([]any)
	if len(rows) != len(provider.entries) {
		t.Fatal("history rows missing", rows)
	}
	for i, value := range rows {
		row := value.(map[string]any)
		want := provider.entries[i]
		if row["transaction_type"] != strconv.Itoa(want.Type) || row["balance_change_free"] != strconv.FormatInt(want.Free, 10) || row["balance_change_charge"] != strconv.FormatInt(want.Paid, 10) || row["date"] != "2026-10-07T04:06:23+0900" {
			t.Fatal("incorrect transaction wire types", row)
		}
		raw, err := json.Marshal(want.Reason)
		if err != nil || row["reason"] != string(raw) {
			t.Fatal("reason is not the JSON-encoded string", row, err)
		}
		if want.Type == gem.TransactionIssue {
			if row["expire_charge"] != "" || row["expire_free"] != "" {
				t.Fatal("issuance expiration fields missing", row)
			}
		} else if _, exists := row["expire_charge"]; exists {
			t.Fatal("consume gained issuance fields", row)
		}
	}
}

func TestShimQuartzHistoryPagesAndResolvedBinding(t *testing.T) {
	provider := &historyBalance{}
	for i := range 21 {
		provider.entries = append(provider.entries, gem.Transaction{Type: gem.TransactionIssue, Date: time.Now(), Free: int64(i + 1)})
	}
	st := NewMemoryStore()
	if err := st.BindRequestor(context.Background(), "synthetic-device", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab"); err != nil {
		t.Fatal(err)
	}
	sh := NewShim(NewService(st, nil, "", false), provider, nil)
	for _, page := range []struct {
		path   string
		size   int
		more   bool
		offset int
	}{
		{"/payment/history?offset=0&limit=20", 20, true, 0},
		{"/payment/history?offset=20&limit=20", 1, false, 20},
		{"/payment/history?offset=21&limit=20", 0, false, 21},
	} {
		entry := historyResponse(t, historyRequest(sh, page.path, "synthetic-device"))
		if len(entry["transactions"].([]any)) != page.size || entry["has_next"] != page.more || provider.offset != page.offset || provider.limit != 20 || provider.xuid != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab" {
			t.Fatal(page, entry, provider)
		}
	}
}

func TestShimQuartzHistoryRejectsInvalidPagination(t *testing.T) {
	provider := &historyBalance{}
	sh := NewShim(nil, provider, nil)
	for _, query := range []string{
		"offset=", "offset=-1", "offset=%2B1", "offset=1.5", "offset=word", "offset=2147483648", "offset=1&offset=2",
		"limit=", "limit=0", "limit=101", "limit=-1", "limit=20&limit=20", "limit=999999999999999999999999", "limit=%zz", "offset=0;limit=20",
	} {
		t.Run(query, func(t *testing.T) {
			requireShimFailure(t, historyRequest(sh, "/payment/history?"+query, "lilypad-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab"))
		})
	}
	if provider.calls != 0 {
		t.Fatal("invalid pagination reached storage", provider.calls)
	}
	entry := historyResponse(t, historyRequest(sh, "/payment/history?offset=000&limit=020", "lilypad-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab"))
	if entry["offset"] != "0" || entry["limit"] != "20" {
		t.Fatal("pagination was not normalized", entry)
	}
}

func TestShimQuartzHistoryFailsClosed(t *testing.T) {
	for _, provider := range []BalanceProvider{fakeBalance{}, unavailableBalance{}, &historyBalance{err: errors.New("journal unavailable")}} {
		requireShimFailure(t, historyRequest(NewShim(nil, provider, nil), "/payment/history", "lilypad-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab"))
	}
	provider := &historyBalance{}
	sh := NewShim(nil, provider, nil)
	for _, rid := range []string{"", "unbound-device", "lilypad-42"} {
		requireShimFailure(t, historyRequest(sh, "/payment/history", rid))
	}
	svc := NewService(failingRequestorStore{Store: NewMemoryStore(), lookupFailure: true}, nil, "", false)
	requireShimFailure(t, historyRequest(NewShim(svc, provider, nil), "/payment/history", "lilypad-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab"))
	if provider.calls != 0 {
		t.Fatal("failed identity lookup reached storage")
	}
	entry := historyResponse(t, historyRequest(NewShim(nil, nil, nil), "/payment/history", "lilypad-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab"))
	if len(entry["transactions"].([]any)) != 0 || entry["has_next"] != false {
		t.Fatal("development mode invented transactions", entry)
	}
}

func TestShimQuartzHistoryRejectsCorruptTransactions(t *testing.T) {
	for _, row := range []gem.Transaction{
		{Type: 2, Date: time.Now(), Paid: 20},
		{Type: gem.TransactionConsume, Date: time.Now(), Free: 1},
		{Type: gem.TransactionIssue, Date: time.Now(), Free: 1, Paid: 1},
		{Type: gem.TransactionIssue, Date: time.Now(), Free: 1, Reason: gem.Reason{Type: 99}},
		{Type: gem.TransactionIssue, Free: 1},
	} {
		provider := &historyBalance{entries: []gem.Transaction{row}}
		requireShimFailure(t, historyRequest(NewShim(nil, provider, nil), "/payment/history", "lilypad-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab"))
	}
}

func TestShimQuartzHistoryDoesNotInventMissingMasterTitles(t *testing.T) {
	provider := &historyBalance{entries: []gem.Transaction{{Type: gem.TransactionIssue, Date: time.Now(), Free: 100, Reason: gem.Reason{Type: gem.ReasonServerGift, MasterReasonID: 900}}}}
	entry := historyResponse(t, historyRequest(NewShim(nil, provider, nil), "/payment/history", "lilypad-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab"))
	row := entry["transactions"].([]any)[0].(map[string]any)
	if !strings.Contains(row["reason"].(string), `"masterReasonId":900`) {
		t.Fatal("missing server master changed persisted reason", row)
	}
}
