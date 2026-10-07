package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	"lilypad/internal/gem"
	"lilypad/internal/store"
)

func (s *Store) ExportSnapshot(ctx context.Context, uid int64) (store.Snapshot, error) {
	snapshot := store.Snapshot{Tables: map[string][]Row{}, Hashes: map[string]string{}}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockPlayer(ctx, tx, uid); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT table_name,rows,hash FROM user_state WHERE user_id=$1`, uid)
		if err != nil {
			return err
		}
		for rows.Next() {
			var name, hash string
			var raw []byte
			if err := rows.Scan(&name, &raw, &hash); err != nil {
				rows.Close()
				return err
			}
			parsed, err := unmarshalRows(raw)
			if err != nil {
				rows.Close()
				return err
			}
			snapshot.Tables[name] = parsed
			snapshot.Hashes[name] = hash
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `SELECT free,paid FROM gems WHERE user_id=$1`, uid).Scan(&snapshot.Balance.Free, &snapshot.Balance.Paid)
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	})
	return snapshot, err
}

func (s *Store) ImportSnapshot(ctx context.Context, uid int64, tables map[string][]Row, balance *gem.Balance) error {
	if balance != nil {
		if err := balance.Validate(); err != nil {
			return err
		}
	}
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockPlayer(ctx, tx, uid); err != nil {
			return err
		}
		inner := context.WithValue(ctx, transactionKey{}, tx)
		if err := s.ImportTables(inner, uid, tables); err != nil {
			return err
		}
		if balance != nil {
			if _, err := s.Set(inner, uid, *balance); err != nil {
				return err
			}
		}
		return nil
	})
}
