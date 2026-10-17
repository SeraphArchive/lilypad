package postgres

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

	"lilypad/internal/deltacomm"
	"lilypad/internal/gem"
	"lilypad/internal/store"
)

func TestPGQuartzHistorySettlementAndRecoverySeparation(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t, nil)
	acc, err := s.GetOrCreate(ctx, "synthetic-quartz-history")
	if err != nil {
		t.Fatal(err)
	}
	uid := acc.UserID
	if _, err := s.Set(ctx, uid, gem.Balance{Free: 25, Paid: 40}); err != nil {
		t.Fatal(err)
	}
	entries, more, err := s.History(ctx, uid, 0, 20)
	if err != nil || entries == nil || len(entries) != 0 || more {
		t.Fatal("Set fabricated game history", entries, more, err)
	}
	_, err = s.MutateWithGems(ctx, uid, nil, func(map[string][]Row) (deltacomm.Deltas, store.GemChange, error) {
		return deltacomm.Deltas{}, store.GemChange{
			Consume: 30, ConsumeReason: gem.Reason{Type: gem.ReasonLottery, MasterLotteryID: 100}, Add: 50,
			Credits: []gem.Credit{
				{Amount: 20, Reason: gem.Reason{Type: gem.ReasonMission, MasterMissionID: 500}},
				{Amount: 30, Reason: gem.Reason{Type: gem.ReasonServerGift, MasterReasonID: 900}},
			},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, more, err = s.History(ctx, uid, 0, 20)
	if err != nil || more || len(entries) != 3 {
		t.Fatal(entries, more, err)
	}
	if entries[0].Free != 30 || entries[0].Reason.MasterReasonID != 900 || entries[1].Free != 20 || entries[1].Reason.MasterMissionID != 500 || entries[2].Free != -25 || entries[2].Paid != -5 || entries[2].Reason.MasterLotteryID != 100 {
		t.Fatal("settlement provenance or actual debit split lost", entries)
	}
	if !entries[0].Date.Equal(entries[1].Date) {
		t.Fatal("credit batch timestamps differ")
	}
	balance, err := s.Get(ctx, uid)
	if err != nil || balance != (gem.Balance{Free: 50, Paid: 35}) {
		t.Fatal(balance, err)
	}
	before := append([]gem.Transaction{}, entries...)
	if _, err := s.Add(ctx, uid, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(ctx, uid, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consume(ctx, uid, 1000); !errors.Is(err, gem.ErrInsufficient) {
		t.Fatal(err)
	}
	if _, err := s.Set(ctx, uid, gem.Balance{Free: 100, Paid: 100}); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportSnapshot(ctx, uid, map[string][]Row{}, &gem.Balance{Free: 77}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE gems SET free=70 WHERE user_id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	entries, more, err = s.History(ctx, uid, 0, 20)
	if err != nil || more || !reflect.DeepEqual(before, entries) {
		t.Fatal("replacement erased or fabricated history", entries, err)
	}
	var recovery int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM player_data_history WHERE user_id=$1 AND table_name='@gems'`, uid).Scan(&recovery); err != nil || recovery < 6 {
		t.Fatal("wallet recovery history missing", recovery, err)
	}
	for _, query := range []string{
		`UPDATE gem_transactions SET reason='{}' WHERE user_id=$1`,
		`DELETE FROM gem_transactions WHERE user_id=$1`,
	} {
		if _, err := s.pool.Exec(ctx, query, uid); err == nil {
			t.Fatal("external journal edit accepted", query)
		}
	}
	if _, err := s.pool.Exec(ctx, `TRUNCATE gem_transactions`); err == nil {
		t.Fatal("journal truncate accepted")
	}
}

func TestPGQuartzHistoryConcurrentConsumeAndPages(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t, nil)
	acc, err := s.GetOrCreate(ctx, "synthetic-quartz-pages")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set(ctx, acc.UserID, gem.Balance{Free: 21}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var succeeded atomic.Int64
	for range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Consume(ctx, acc.UserID, 1); err == nil {
				succeeded.Add(1)
			} else if !errors.Is(err, gem.ErrInsufficient) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if succeeded.Load() != 21 {
		t.Fatal("unexpected concurrent settlements", succeeded.Load())
	}
	for _, page := range []struct {
		offset, size int
		more         bool
	}{{0, 20, true}, {20, 1, false}, {21, 0, false}} {
		entries, more, err := s.History(ctx, acc.UserID, page.offset, 20)
		if err != nil || entries == nil || len(entries) != page.size || more != page.more {
			t.Fatal(page, entries, more, err)
		}
		for _, entry := range entries {
			if entry.Type != gem.TransactionConsume || entry.Free != -1 || entry.Paid != 0 || entry.Reason.Type != gem.ReasonNone {
				t.Fatal(entry)
			}
		}
	}
	other, err := s.GetOrCreate(ctx, "synthetic-quartz-other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, other.UserID, 50); err != nil {
		t.Fatal(err)
	}
	entries, more, err := s.History(ctx, other.UserID, 0, 20)
	if err != nil || more || len(entries) != 1 || entries[0].Free != 50 || entries[0].Reason.Type != gem.ReasonNone {
		t.Fatal("other account history mixed", entries, more, err)
	}
}

func TestPGQuartzHistoryFailuresRollbackWalletAndJournal(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t, nil)
	acc, err := s.GetOrCreate(ctx, "synthetic-quartz-rollback")
	if err != nil {
		t.Fatal(err)
	}
	uid := acc.UserID
	if _, err := s.Set(ctx, uid, gem.Balance{Free: 100}); err != nil {
		t.Fatal(err)
	}
	// The outer request may commit a failure response; its inner savepoint must
	// still discard the debit and journal when applying reward rows fails.
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		inner := context.WithValue(ctx, transactionKey{}, tx)
		_, err := s.MutateWithGems(inner, uid, nil, func(map[string][]Row) (deltacomm.Deltas, store.GemChange, error) {
			return deltacomm.Deltas{PutItems: map[string][]Row{"user_story": {{"_isClear": true}}}}, store.GemChange{Consume: 10}, nil
		})
		if err == nil {
			t.Fatal("invalid reward rows accepted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `
		CREATE FUNCTION fail_quartz_journal() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'synthetic journal failure'; END $$;
		CREATE TRIGGER fail_quartz_journal BEFORE INSERT ON gem_transactions
		FOR EACH ROW EXECUTE FUNCTION fail_quartz_journal()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.pool.Exec(ctx, `DROP TRIGGER fail_quartz_journal ON gem_transactions; DROP FUNCTION fail_quartz_journal()`); err != nil {
			t.Error(err)
		}
	})
	if _, err := s.Add(ctx, uid, 50); err == nil {
		t.Fatal("failed journal acknowledged a credit")
	}
	if _, err := s.Consume(ctx, uid, 20); err == nil {
		t.Fatal("failed journal acknowledged a debit")
	}
	balance, err := s.Get(ctx, uid)
	if err != nil || balance != (gem.Balance{Free: 100}) {
		t.Fatal("failure changed wallet", balance, err)
	}
	entries, more, err := s.History(ctx, uid, 0, 20)
	if err != nil || more || len(entries) != 0 {
		t.Fatal("failed mutation left journal rows", entries, more, err)
	}
	var recovery int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM player_data_history WHERE user_id=$1 AND table_name='@gems'`, uid).Scan(&recovery); err != nil || recovery != 1 {
		t.Fatal("failed mutation left recovery snapshots", recovery, err)
	}
}
