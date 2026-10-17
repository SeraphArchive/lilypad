package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"lilypad/internal/account"
	"lilypad/internal/gem"
)

type historyReader struct {
	uid           int64
	offset, limit int
	entries       []gem.Transaction
	err           error
}

func (h *historyReader) History(_ context.Context, uid int64, offset, limit int) ([]gem.Transaction, bool, error) {
	h.uid, h.offset, h.limit = uid, offset, limit
	return h.entries, false, h.err
}

func TestGemHistoryAdapterResolvesXUIDToNumericAccount(t *testing.T) {
	ctx := context.Background()
	accounts := account.NewMemory(0)
	acc, err := accounts.GetOrCreate(ctx, "synthetic-history-account")
	if err != nil {
		t.Fatal(err)
	}
	history := &historyReader{entries: []gem.Transaction{{Type: gem.TransactionIssue, Date: time.Now(), Free: 20}}}
	adapter := gemBalanceAdapter{accounts: accounts, histories: history}
	entries, more, err := adapter.GemHistory(ctx, acc.XUID, 20, 5)
	if err != nil || more || !reflect.DeepEqual(entries, history.entries) || history.uid != acc.UserID || history.offset != 20 || history.limit != 5 {
		t.Fatal("history adapter changed identity or pagination", entries, history, err)
	}
	history.err = errors.New("journal unavailable")
	if _, _, err := adapter.GemHistory(ctx, acc.XUID, 0, 20); err == nil {
		t.Fatal("history storage failure ignored")
	}
	if _, _, err := (gemBalanceAdapter{accounts: accounts}).GemHistory(ctx, acc.XUID, 0, 20); err == nil {
		t.Fatal("missing history storage acknowledged")
	}
}
