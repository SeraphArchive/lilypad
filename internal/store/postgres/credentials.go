package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"lilypad/internal/portal"
)

var _ portal.Store = (*Store)(nil)

func (s *Store) CreateCredential(ctx context.Context, rec portal.CredentialRecord) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO credentials
		  (email, password_hash, email_verified, verification_token,
		   migration_code, migration_password_hash, xuid,verification_sent_at)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,$6,$7,CASE WHEN $4='' THEN NULL ELSE now() END)`,
		rec.Email, rec.PasswordHash, rec.EmailVerified, rec.VerificationToken,
		rec.MigrationCode, rec.MigrationPasswordHash, rec.XUID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return portal.ErrEmailTaken
		}
		return fmt.Errorf("postgres: CreateCredential: %w", err)
	}
	return nil
}

func scanCredential(row pgx.Row) (portal.CredentialRecord, error) {
	var rec portal.CredentialRecord
	var token, migCode, migPwHash *string
	var sentAt *time.Time
	err := row.Scan(&rec.Email, &rec.PasswordHash, &rec.EmailVerified, &token,
		&migCode, &migPwHash, &rec.XUID, &sentAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return portal.CredentialRecord{}, portal.ErrNotFound
	} else if err != nil {
		return portal.CredentialRecord{}, err
	}
	rec.VerificationToken = deref(token)
	rec.MigrationCode = deref(migCode)
	rec.MigrationPasswordHash = deref(migPwHash)
	if sentAt != nil {
		rec.VerificationSentAt = *sentAt
	}
	return rec, nil
}

const credentialCols = `email, password_hash, email_verified, verification_token,
	migration_code, migration_password_hash, xuid, verification_sent_at`

func (s *Store) GetCredentialByEmail(ctx context.Context, email string) (portal.CredentialRecord, error) {
	return scanCredential(s.pool.QueryRow(ctx,
		`SELECT `+credentialCols+` FROM credentials WHERE email=$1`, email))
}

func (s *Store) FindByVerificationToken(ctx context.Context, token string) (portal.CredentialRecord, error) {
	return scanCredential(s.pool.QueryRow(ctx,
		`SELECT `+credentialCols+` FROM credentials WHERE verification_token=$1`, token))
}

func (s *Store) FindByMigrationCode(ctx context.Context, code string) (portal.CredentialRecord, error) {
	return scanCredential(s.pool.QueryRow(ctx,
		`SELECT `+credentialCols+` FROM credentials WHERE migration_code=$1`, code))
}

func (s *Store) ConsumeVerificationToken(ctx context.Context, token string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE credentials SET email_verified=TRUE, verification_token=NULL
		 WHERE verification_token=$1 AND $1<>'' AND NOT email_verified
		 AND verification_sent_at>statement_timestamp()-INTERVAL '24 hours'
		 AND verification_sent_at<=statement_timestamp()`, token)
	if err != nil {
		return fmt.Errorf("postgres: ConsumeVerificationToken: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return portal.ErrNotFound
	}
	return nil
}

func (s *Store) UpdateMigrationPasswordHash(ctx context.Context, email, hash string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE credentials SET migration_password_hash=$2 WHERE email=$1`, email, hash)
	if err != nil {
		return fmt.Errorf("postgres: UpdateMigrationPasswordHash: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return portal.ErrNotFound
	}
	return nil
}

func (s *Store) UpdateVerification(ctx context.Context, email, token string) error {
	result, err := s.pool.Exec(ctx, `UPDATE credentials SET verification_token=$2,verification_sent_at=statement_timestamp()
		WHERE email=$1 AND NOT email_verified AND
		(verification_sent_at IS NULL OR verification_sent_at<=statement_timestamp()-INTERVAL '1 minute')`, email, token)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return portal.ErrNotFound
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
