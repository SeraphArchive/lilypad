package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"lilypad/internal/gem"
)

var _ gem.HistoryStore = (*Store)(nil)

func (s *Store) History(ctx context.Context, userID int64, offset, limit int) ([]gem.Transaction, bool, error) {
	if err := gem.ValidateHistoryPage(offset, limit); err != nil {
		return nil, false, err
	}
	rows, err := s.database(ctx).Query(ctx, `
		SELECT transaction_type, changed_at, free_delta, paid_delta, reason
		FROM gem_transactions WHERE user_id=$1 ORDER BY id DESC LIMIT $2 OFFSET $3`, userID, limit+1, offset)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: gem history: %w", err)
	}
	defer rows.Close()
	entries := make([]gem.Transaction, 0, limit+1)
	for rows.Next() {
		var entry gem.Transaction
		var raw []byte
		if err := rows.Scan(&entry.Type, &entry.Date, &entry.Free, &entry.Paid, &raw); err != nil {
			return nil, false, fmt.Errorf("postgres: gem history row: %w", err)
		}
		if err := json.Unmarshal(raw, &entry.Reason); err != nil {
			return nil, false, fmt.Errorf("postgres: gem history reason: %w", err)
		}
		if err := entry.Reason.Validate(); err != nil {
			return nil, false, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("postgres: gem history: %w", err)
	}
	if len(entries) > limit {
		return entries[:limit], true, nil
	}
	return entries, false, nil
}

func recordGemTransactionsTx(ctx context.Context, tx pgx.Tx, userID int64, entries []gem.Transaction) error {
	type entry struct {
		Type   int        `json:"transaction_type"`
		Free   int64      `json:"free_delta"`
		Paid   int64      `json:"paid_delta"`
		Reason gem.Reason `json:"reason"`
	}
	batch := make([]entry, 0, len(entries))
	for _, e := range entries {
		batch = append(batch, entry{Type: e.Type, Free: e.Free, Paid: e.Paid, Reason: e.Reason})
	}
	raw, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("postgres: gem transaction reason: %w", err)
	}
	// One statement gives the batch one timestamp and preserves claim order for
	// ID allocation; the wallet and these rows share the caller's transaction.
	_, err = tx.Exec(ctx, `
		INSERT INTO gem_transactions(user_id, transaction_type, free_delta, paid_delta, reason)
		SELECT $1, (entry->>'transaction_type')::integer, (entry->>'free_delta')::bigint,
		       (entry->>'paid_delta')::bigint, entry->'reason'
		FROM jsonb_array_elements($2::jsonb) WITH ORDINALITY AS batch(entry, ordinal)
		ORDER BY ordinal`, userID, raw)
	if err != nil {
		return fmt.Errorf("postgres: record gem transactions: %w", err)
	}
	return nil
}
