package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"lilypad/internal/clientversion"
)

// *Store implements clientversion.GlobalStore: the "latest seen" installed
// build lives in the same database, as a single row (id=1).
var _ clientversion.GlobalStore = (*Store)(nil)

// GetClientVersion returns the stored latest report; an absent row is nil.
func (s *Store) GetClientVersion(ctx context.Context) (*clientversion.Report, error) {
	var r clientversion.Report
	err := s.pool.QueryRow(ctx,
		`SELECT program_version, asset_version, asset_hash FROM client_version WHERE id = 1`,
	).Scan(&r.ProgramVersion, &r.AssetVersion, &r.AssetHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get client version: %w", err)
	}
	return &r, nil
}

// SetClientVersion upserts the latest report into the single row.
func (s *Store) SetClientVersion(ctx context.Context, r clientversion.Report) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO client_version (id, program_version, asset_version, asset_hash)
		VALUES (1, $1, $2, $3)
		ON CONFLICT (id) DO UPDATE SET
			program_version = EXCLUDED.program_version,
			asset_version   = EXCLUDED.asset_version,
			asset_hash      = EXCLUDED.asset_hash,
			updated_at      = now()`,
		r.ProgramVersion, r.AssetVersion, r.AssetHash,
	); err != nil {
		return fmt.Errorf("postgres: set client version: %w", err)
	}
	return nil
}
