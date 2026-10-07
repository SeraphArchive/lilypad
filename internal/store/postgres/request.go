package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"lilypad/internal/deltacomm"
	"lilypad/internal/store"
)

type transactionKey struct{}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *Store) database(ctx context.Context) queryer {
	if tx, ok := ctx.Value(transactionKey{}).(pgx.Tx); ok {
		return tx
	}
	return s.pool
}

// Nested mutations use savepoints, so handlers may report an error without
// accidentally committing a partial inner mutation in the request transaction.
func (s *Store) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	if tx, ok := ctx.Value(transactionKey{}).(pgx.Tx); ok {
		return pgx.BeginFunc(ctx, tx, fn)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('lilypad.internal_write','on',true)`); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (s *Store) CheckSession(ctx context.Context, uid int64, token string, msgid int64) (int64, error) {
	if token == "" {
		return 0, store.ErrSession
	}
	var generation int64
	var active string
	var revoked, retired bool
	err := s.database(ctx).QueryRow(ctx, `SELECT session_generation, session_token, session_revoked, retired_sessions ? $2 FROM accounts WHERE user_id=$1`, uid, token).Scan(&generation, &active, &revoked, &retired)
	if err != nil {
		return 0, err
	}
	if revoked || retired || active == "" || token != active {
		return 0, store.ErrSession
	}
	return generation, nil
}

func (s *Store) BeginSession(ctx context.Context, uid int64, token string, base int64) error {
	if token == "" || len(token) > 128 {
		return store.ErrSession
	}
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockPlayer(ctx, tx, uid); err != nil {
			return err
		}
		var raw []byte
		var retired bool
		if err := tx.QueryRow(ctx, `SELECT session_history,retired_sessions ? $2 FROM accounts WHERE user_id=$1`, uid, token).Scan(&raw, &retired); err != nil {
			return err
		}
		if retired {
			return store.ErrSession
		}
		history := map[string]int64{}
		if err := json.Unmarshal(raw, &history); err != nil {
			return err
		}
		if token != "" {
			if previous, known := history[token]; known && base <= previous {
				return nil
			}
			history[token] = base
		}
		historyJSON, err := json.Marshal(history)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE accounts SET
    session_generation=session_generation + CASE WHEN session_revoked OR session_token<>$2 OR $3>session_base THEN 1 ELSE 0 END,
    session_token=$2, session_revoked=FALSE, session_base=GREATEST(session_base,$3),session_history=$4
			WHERE user_id=$1 AND (session_revoked OR $3>=session_base OR $2<>'')`, uid, token, base, historyJSON)
		return err
	})
}

func (s *Store) RunRequest(ctx context.Context, uid, generation int64, key, digest string, fn func(context.Context) (store.Response, error)) (store.Response, error) {
	var response store.Response
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockPlayer(ctx, tx, uid); err != nil {
			return err
		}
		var current int64
		var revoked, retired bool
		var active string
		if err := tx.QueryRow(ctx, `SELECT session_generation,session_revoked,session_token,retired_sessions ? session_token FROM accounts WHERE user_id=$1`, uid).Scan(&current, &revoked, &active, &retired); err != nil {
			return err
		}
		if revoked || retired || active == "" || current != generation {
			return store.ErrSession
		}
		if key != "" {
			key = active + ":" + key
			var savedDigest string
			var raw []byte
			err := tx.QueryRow(ctx, `SELECT digest,response FROM request_results WHERE user_id=$1 AND request_key=$2 ORDER BY generation DESC LIMIT 1`, uid, key).Scan(&savedDigest, &raw)
			if err == nil {
				if savedDigest != digest {
					return store.ErrRequestConflict
				}
				return json.Unmarshal(raw, &response)
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var err error
		response, err = fn(context.WithValue(ctx, transactionKey{}, tx))
		if err != nil {
			return err
		}
		if response.Status >= 500 {
			return fmt.Errorf("request failed before committing")
		}
		if key != "" {
			raw, err := json.Marshal(response)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO request_results(user_id,generation,request_key,digest,response) VALUES($1,$2,$3,$4,$5)`, uid, generation, key, digest, raw); err != nil {
				return err
			}
			// Do not expire a paid operation and make a delayed retry debit again.
			return nil
		}
		return nil
	})
	return response, err
}

func repairHashes(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT user_id,table_name,rows FROM user_state`)
	if err != nil {
		return err
	}
	type repair struct {
		uid         int64
		table, hash string
	}
	var updates []repair
	for rows.Next() {
		var uid int64
		var table string
		var raw []byte
		if err := rows.Scan(&uid, &table, &raw); err != nil {
			rows.Close()
			return err
		}
		hash := ""
		if !deltacomm.HashExcluded(table) {
			parsed, err := unmarshalRows(raw)
			if err != nil {
				rows.Close()
				return err
			}
			hash, err = deltacomm.HashTable(parsed)
			if err != nil {
				rows.Close()
				return err
			}
		}
		updates = append(updates, repair{uid, table, hash})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, u := range updates {
		if _, err := tx.Exec(ctx, `UPDATE user_state SET hash=$3 WHERE user_id=$1 AND table_name=$2`, u.uid, u.table, u.hash); err != nil {
			return err
		}
	}
	return nil
}
