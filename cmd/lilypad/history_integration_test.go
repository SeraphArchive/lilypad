package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"lilypad/internal/deltacomm"
	"lilypad/internal/gem"
	"lilypad/internal/portal"
	"lilypad/internal/store"
	"lilypad/internal/store/postgres"
	"lilypad/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Run(m.Run)) }

func TestPaymentHistoryThroughProductionAdapterAndDispatch(t *testing.T) {
	dsn := os.Getenv("LILYPAD_TEST_DSN")
	if dsn == "" {
		t.Skip("LILYPAD_TEST_DSN not set")
	}
	ctx := context.Background()
	pg, err := postgres.New(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	firstXUID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab"
	otherXUID := strings.Repeat("b", 31) + "c"
	first, err := pg.GetOrCreate(ctx, firstXUID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := pg.GetOrCreate(ctx, otherXUID)
	if err != nil {
		t.Fatal(err)
	}
	if err := pg.BindRequestor(ctx, "synthetic-history-first", firstXUID); err != nil {
		t.Fatal(err)
	}
	if err := pg.BindRequestor(ctx, "synthetic-history-other", otherXUID); err != nil {
		t.Fatal(err)
	}
	credits := []gem.Credit{}
	for i := range 21 {
		credits = append(credits, gem.Credit{Amount: 1, Reason: gem.Reason{Type: gem.ReasonMission, MasterMissionID: int64(500 + i)}})
	}
	if _, err := pg.MutateWithGems(ctx, first.UserID, nil, func(map[string][]store.Row) (deltacomm.Deltas, store.GemChange, error) {
		return deltacomm.Deltas{}, store.GemChange{Add: 21, Credits: credits}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Set(ctx, other.UserID, gem.Balance{Free: 999}); err != nil {
		t.Fatal(err)
	}
	adapter := gemBalanceAdapter{accounts: pg, gems: pg, histories: pg}
	shim := portal.NewShim(portal.NewService(pg, nil, "", false), adapter, nil)
	root := dispatch(http.NotFoundHandler(), http.NotFoundHandler(), shim.Routes())
	for _, page := range []struct {
		requestor, query string
		count            int
		more             bool
		firstMission     int64
	}{
		{"synthetic-history-first", "offset=0&limit=20", 20, true, 520},
		{"synthetic-history-first", "offset=20&limit=20", 1, false, 500},
		{"synthetic-history-other", "offset=0&limit=20", 0, false, 0},
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1.0/payment/history?"+page.query, nil)
		req.Header.Set("Authorization", `OAuth xoauth_requestor_id="`+page.requestor+`"`)
		rec := httptest.NewRecorder()
		root.ServeHTTP(rec, req)
		var response struct {
			Result string `json:"result"`
			Entry  struct {
				Offset       string `json:"offset"`
				Limit        string `json:"limit"`
				HasNext      bool   `json:"has_next"`
				Transactions []struct {
					Type   string `json:"transaction_type"`
					Free   string `json:"balance_change_free"`
					Paid   string `json:"balance_change_charge"`
					Reason string `json:"reason"`
					Date   string `json:"date"`
				} `json:"transactions"`
			} `json:"entry"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusOK || response.Result != "OK" || response.Entry.Limit != "20" || response.Entry.HasNext != page.more || response.Entry.Transactions == nil || len(response.Entry.Transactions) != page.count || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("production history dispatch", page, rec.Code, rec.Body.String())
		}
		if page.count > 0 {
			tx := response.Entry.Transactions[0]
			var reason gem.Reason
			if err := json.Unmarshal([]byte(tx.Reason), &reason); err != nil || reason.Type != gem.ReasonMission || reason.MasterMissionID != page.firstMission || reason.MasterReasonID != 0 || tx.Type != "3" || tx.Free != "1" || tx.Paid != "0" || !strings.HasSuffix(tx.Date, "+0900") {
				t.Fatal("journal to platform conversion", tx, reason, err)
			}
		}
	}
}
