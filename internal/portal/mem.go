package portal

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"net/smtp"
	"sync"
	"time"
)

// LogMailer records verification links to the logger instead of sending email.
// Suitable for development and the verification-disabled default.
type LogMailer struct{ Log *slog.Logger }

func (m LogMailer) SendVerification(_ context.Context, email, link string) error {
	log := m.Log
	if log == nil {
		log = slog.Default()
	}
	log.Info("portal verification (dev)", "email", email, "link", link)
	return nil
}

// SMTPMailer sends verification email via SMTP for production use.
type SMTPMailer struct {
	Addr string // host:port
	Auth smtp.Auth
	From string
}

func (m SMTPMailer) SendVerification(ctx context.Context, email, link string) error {
	msg := "From: " + m.From + "\r\n" +
		"To: " + email + "\r\n" +
		"Subject: Verify your LilyPad account\r\n\r\n" +
		"Confirm your account by visiting:\r\n" + link + "\r\n"
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", m.Addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	host, _, err := net.SplitHostPort(m.Addr)
	if err != nil {
		return err
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if m.Auth != nil {
		if err := client.Auth(m.Auth); err != nil {
			return err
		}
	}
	if err := client.Mail(m.From); err != nil {
		return err
	}
	if err := client.Rcpt(email); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// MemoryStore is an in-memory Store for tests and the no-database dev mode.
type MemoryStore struct {
	mu              sync.Mutex
	byEmail         map[string]CredentialRecord
	xuidByRequestor map[string]string
}

// NewMemoryStore returns an empty in-memory credential store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byEmail: map[string]CredentialRecord{}, xuidByRequestor: map[string]string{}}
}

func (s *MemoryStore) CreateCredential(_ context.Context, rec CredentialRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byEmail[rec.Email]; ok {
		return ErrEmailTaken
	}
	s.byEmail[rec.Email] = rec
	return nil
}

func (s *MemoryStore) GetCredentialByEmail(_ context.Context, email string) (CredentialRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byEmail[email]
	if !ok {
		return CredentialRecord{}, ErrNotFound
	}
	return rec, nil
}

func (s *MemoryStore) FindByVerificationToken(_ context.Context, token string) (CredentialRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.byEmail {
		if rec.VerificationToken != "" && rec.VerificationToken == token {
			return rec, nil
		}
	}
	return CredentialRecord{}, ErrNotFound
}

func (s *MemoryStore) FindByMigrationCode(_ context.Context, code string) (CredentialRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.byEmail {
		if rec.MigrationCode == code {
			return rec, nil
		}
	}
	return CredentialRecord{}, ErrNotFound
}

func (s *MemoryStore) ConsumeVerificationToken(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for email, rec := range s.byEmail {
		if token != "" && rec.VerificationToken == token && !rec.EmailVerified &&
			!rec.VerificationSentAt.IsZero() && rec.VerificationSentAt.After(now.Add(-24*time.Hour)) &&
			!rec.VerificationSentAt.After(now) {
			rec.EmailVerified = true
			rec.VerificationToken = ""
			s.byEmail[email] = rec
			return nil
		}
	}
	return ErrNotFound
}

func (s *MemoryStore) UpdateMigrationPasswordHash(_ context.Context, email, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byEmail[email]
	if !ok {
		return ErrNotFound
	}
	rec.MigrationPasswordHash = hash
	s.byEmail[email] = rec
	return nil
}

func (s *MemoryStore) UpdateVerification(_ context.Context, email, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byEmail[email]
	if !ok || rec.EmailVerified || time.Since(rec.VerificationSentAt) < time.Minute {
		return ErrNotFound
	}
	rec.VerificationToken = token
	rec.VerificationSentAt = time.Now()
	s.byEmail[email] = rec
	return nil
}

func (s *MemoryStore) BindRequestor(_ context.Context, requestorID, xuid string) error {
	if requestorID == "" || xuid == "" {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.xuidByRequestor[requestorID] = xuid
	return nil
}

func (s *MemoryStore) XUIDForRequestor(_ context.Context, requestorID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	xuid, ok := s.xuidByRequestor[requestorID]
	if !ok || xuid == "" {
		return "", ErrNotFound
	}
	return xuid, nil
}

var _ Store = (*MemoryStore)(nil)
