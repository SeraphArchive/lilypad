package postgres

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
)

func (s *Store) RecordRejectedSave(ctx context.Context, uid int64, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO player_data_history(user_id,table_name,operation,new_data,source) VALUES($1,'@rejected_push','REJECTED',$2,'client_rejected')`, uid, raw)
		return err
	})
}
