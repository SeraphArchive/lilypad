package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"lilypad/internal/portal"
)

func (s *Store) BindRequestor(ctx context.Context, requestorID, xuid string) error {
	if requestorID == "" || xuid == "" {
		return portal.ErrNotFound
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO platform_requestor_bindings (requestor_id, x_uid, created_at, updated_at)
		VALUES ($1, $2, now(), now())
		ON CONFLICT (requestor_id)
		DO UPDATE SET x_uid=EXCLUDED.x_uid, updated_at=now()`,
		requestorID, xuid)
	if err != nil {
		return fmt.Errorf("postgres: BindRequestor: %w", err)
	}
	return nil
}

func (s *Store) XUIDForRequestor(ctx context.Context, requestorID string) (string, error) {
	var xuid string
	err := s.pool.QueryRow(ctx,
		`SELECT x_uid FROM platform_requestor_bindings WHERE requestor_id=$1`,
		requestorID).Scan(&xuid)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", portal.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("postgres: XUIDForRequestor: %w", err)
	}
	return xuid, nil
}
