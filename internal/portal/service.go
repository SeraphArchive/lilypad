package portal

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Errors surfaced by the Portal service.
var (
	ErrEmailTaken         = errors.New("portal: email already registered")
	ErrInvalidCredentials = errors.New("portal: invalid email or password")
	ErrNotVerified        = errors.New("portal: email not verified")
	ErrNotFound           = errors.New("portal: not found")
)

// CredentialRecord is the full stored credential row (includes secret hashes;
// never serialize it to clients directly).
type CredentialRecord struct {
	Email                 string
	PasswordHash          string
	EmailVerified         bool
	VerificationToken     string
	VerificationSentAt    time.Time
	MigrationCode         string
	MigrationPasswordHash string
	XUID                  string
}

// Credential is the safe, client-facing view of an account.
type Credential struct {
	Email         string
	EmailVerified bool
	MigrationCode string
	XUID          string
}

// Store is the Portal's credential persistence contract.
type Store interface {
	CreateCredential(ctx context.Context, rec CredentialRecord) error // ErrEmailTaken on dup email
	GetCredentialByEmail(ctx context.Context, email string) (CredentialRecord, error)
	FindByMigrationCode(ctx context.Context, code string) (CredentialRecord, error)
	ConsumeVerificationToken(ctx context.Context, token string) error
	UpdateVerification(ctx context.Context, email, token string) error
	UpdateMigrationPasswordHash(ctx context.Context, email, hash string) error
	BindRequestor(ctx context.Context, requestorID, xuid string) error
	XUIDForRequestor(ctx context.Context, requestorID string) (string, error)
}

// Mailer delivers verification messages. LogMailer (dev) records the link.
type Mailer interface {
	SendVerification(ctx context.Context, email, link string) error
}

// Service holds Portal business logic.
type Service struct {
	store               Store
	mailer              Mailer
	baseURL             string
	requireVerification bool
}

// NewService builds the Portal service.
func NewService(store Store, mailer Mailer, baseURL string, requireVerification bool) *Service {
	return &Service{store: store, mailer: mailer, baseURL: strings.TrimRight(baseURL, "/"), requireVerification: requireVerification}
}

// RegisterResult is returned to a freshly registered user.
type RegisterResult struct {
	XUID                       string
	MigrationCode              string
	MigrationPassword          string // shown once; only the hash is stored
	VerificationLink           string // empty when verification is disabled
	VerificationDeliveryFailed bool
}

// Register creates a credential record, minting a durable XUID and a 引き継ぎ
// code/password. When verification is enabled, a token is generated and mailed.
func (s *Service) Register(ctx context.Context, email, password string) (*RegisterResult, error) {
	email = normalizeEmail(email)
	if email == "" || !strings.Contains(email, "@") || strings.ContainsAny(email, "\r\n") || len(email) > 320 {
		return nil, errors.New("portal: invalid email")
	}
	if len(password) < 8 || len(password) > 1024 {
		return nil, errors.New("portal: password must be at least 8 characters")
	}
	pwHash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	migCode := GenerateMigrationCode()
	migPw := GenerateMigrationPassword()
	// The client sends the migration password reversibly encoded (ROT13∘reverse∘
	// base64), so the server recovers the plaintext and verifies it against this
	// argon2 hash (see credential.go).
	migPwHash, err := HashPassword(migPw)
	if err != nil {
		return nil, err
	}
	rec := CredentialRecord{
		Email:                 email,
		PasswordHash:          pwHash,
		EmailVerified:         !s.requireVerification, // auto-verified when verification is off
		MigrationCode:         migCode,
		MigrationPasswordHash: migPwHash,
		XUID:                  GenerateXUID(),
	}
	res := &RegisterResult{XUID: rec.XUID, MigrationCode: migCode, MigrationPassword: migPw}
	if s.requireVerification {
		rec.VerificationToken = GenerateToken()
		rec.VerificationSentAt = time.Now()
		res.VerificationLink = s.baseURL + "/verify?token=" + rec.VerificationToken
	}
	if err := s.store.CreateCredential(ctx, rec); err != nil {
		return nil, err
	}
	if s.requireVerification && s.mailer != nil {
		if err := s.mailer.SendVerification(ctx, email, res.VerificationLink); err != nil {
			res.VerificationDeliveryFailed = true
		}
	}
	return res, nil
}

// Authenticate validates email+password and (if enabled) verification status.
func (s *Service) Authenticate(ctx context.Context, email, password string) (Credential, error) {
	email = normalizeEmail(email)
	rec, err := s.store.GetCredentialByEmail(ctx, email)
	if errors.Is(err, ErrNotFound) {
		return Credential{}, ErrInvalidCredentials
	} else if err != nil {
		return Credential{}, err
	}
	ok, err := VerifyPassword(rec.PasswordHash, password)
	if err != nil || !ok {
		return Credential{}, ErrInvalidCredentials
	}
	if s.requireVerification && !rec.EmailVerified {
		return Credential{}, ErrNotVerified
	}
	return safe(rec), nil
}

// VerifyEmail marks an account verified given a valid token.
func (s *Service) VerifyEmail(ctx context.Context, token string) error {
	if token == "" {
		return ErrNotFound
	}
	return s.store.ConsumeVerificationToken(ctx, token)
}

// ResetMigrationPassword mints a new 引き継ぎ password, stores only the
// argon2 hash, and returns the plaintext so the Portal can show it once.
func (s *Service) ResetMigrationPassword(ctx context.Context, email string) (string, error) {
	email = normalizeEmail(email)
	if _, err := s.store.GetCredentialByEmail(ctx, email); err != nil {
		return "", err
	}
	migPw := GenerateMigrationPassword()
	migPwHash, err := HashPassword(migPw)
	if err != nil {
		return "", err
	}
	if err := s.store.UpdateMigrationPasswordHash(ctx, email, migPwHash); err != nil {
		return "", err
	}
	return migPw, nil
}

// Account returns the safe view for an email (e.g. for the dashboard).
func (s *Service) Account(ctx context.Context, email string) (Credential, error) {
	rec, err := s.store.GetCredentialByEmail(ctx, normalizeEmail(email))
	if err != nil {
		return Credential{}, err
	}
	return safe(rec), nil
}

// ResolveMigration is used by the platform shim: given a 引き継ぎ code +
// password, return the durable XUID the game then uses.
func (s *Service) ResolveMigration(ctx context.Context, code, migPassword string) (string, error) {
	rec, err := s.store.FindByMigrationCode(ctx, strings.ToUpper(strings.TrimSpace(code)))
	if errors.Is(err, ErrNotFound) {
		return "", ErrInvalidCredentials
	} else if err != nil {
		return "", err
	}
	ok, _ := VerifyMigrationPassword(rec.MigrationPasswordHash, migPassword)
	if !ok {
		return "", ErrInvalidCredentials
	}
	if s.requireVerification && !rec.EmailVerified {
		return "", ErrNotVerified
	}
	return rec.XUID, nil
}

func (s *Service) BindRequestor(ctx context.Context, requestorID, xuid string) error {
	return s.store.BindRequestor(ctx, requestorID, xuid)
}

func (s *Service) ResendVerification(ctx context.Context, email, password string) error {
	if !s.requireVerification || s.mailer == nil {
		return errors.New("verification unavailable")
	}
	rec, err := s.store.GetCredentialByEmail(ctx, normalizeEmail(email))
	if err != nil {
		return ErrInvalidCredentials
	}
	ok, err := VerifyPassword(rec.PasswordHash, password)
	if err != nil || !ok {
		return ErrInvalidCredentials
	}
	if rec.EmailVerified {
		return nil
	}
	if !rec.VerificationSentAt.IsZero() && time.Since(rec.VerificationSentAt) < time.Minute {
		return errors.New("please wait before resending")
	}
	token := GenerateToken()
	if err := s.store.UpdateVerification(ctx, rec.Email, token); err != nil {
		return err
	}
	return s.mailer.SendVerification(ctx, rec.Email, s.baseURL+"/verify?token="+token)
}

func (s *Service) XUIDForRequestor(ctx context.Context, requestorID string) (string, error) {
	return s.store.XUIDForRequestor(ctx, requestorID)
}

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func safe(rec CredentialRecord) Credential {
	return Credential{Email: rec.Email, EmailVerified: rec.EmailVerified, MigrationCode: rec.MigrationCode, XUID: rec.XUID}
}
