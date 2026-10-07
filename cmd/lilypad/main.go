// Command lilypad is the server entrypoint. A single binary serves three
// surfaces, separated by path prefix so one deployment covers everything:
//
//	/api/*, /healthz   game API (codec/envelope/DeltaComm/RPC)
//	/v1.0/*            Gree GameLib platform shim (auth / 引き継ぎ / payment)
//	everything else    LilyPad Portal (email register/login, takeover code)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"lilypad/internal/account"
	"lilypad/internal/config"
	"lilypad/internal/gem"
	"lilypad/internal/httpapi"
	"lilypad/internal/master"
	"lilypad/internal/model"
	"lilypad/internal/portal"
	"lilypad/internal/sign"
	"lilypad/internal/store"
	"lilypad/internal/store/postgres"
)

func main() {
	if err := run(); err != nil {
		slog.Error("lilypad stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "config.local.yaml", "path to config YAML")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Error("load config", "err", err)
		return err
	}

	signer, err := buildSigner(cfg)
	if err != nil {
		log.Error("build signer", "err", err)
		return err
	}

	// With a database configured, the Postgres store provides account
	// resolution, DeltaComm save-sync, and Portal credentials. Without one,
	// fall back to in-memory providers (dev mode; no persistence).
	var (
		accounts    account.Provider
		gameStore   store.Store
		portalStore portal.Store
		gemStore    gem.Store
	)
	var balanceProvider portal.BalanceProvider
	if cfg.DB.DSN != "" {
		// The save's user_version row records the asset build the account was
		// created on (official new accounts carry _bundleVersion). Inject the
		// configured asset version into the seed template.
		seed := model.NewPlayerSeed()
		if cfg.Asset.Version != "" {
			seed["user_version"] = []store.Row{{"_bundleVersion": cfg.Asset.Version}}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		pg, err := postgres.New(ctx, cfg.DB.DSN, seed)
		cancel()
		if err != nil {
			log.Error("connect store", "err", err)
			return err
		}
		defer pg.Close()
		accounts, gameStore, portalStore, gemStore = pg, pg, pg, pg
		balanceProvider = gemBalanceAdapter{accounts: pg, gems: pg}
		log.Info("postgres store ready")
	} else {
		accounts = account.NewMemory(0)
		portalStore = portal.NewMemoryStore()
		log.Warn("no db.dsn configured: in-memory accounts, no save-sync, non-durable portal")
	}

	md, err := loadMaster(cfg, log)
	if err != nil {
		return err
	}
	game := httpapi.New(cfg, signer, accounts, gameStore, md, log)

	portalSvc := portal.NewService(portalStore, buildMailer(cfg, log), cfg.Portal.BaseURL, cfg.Portal.RequireEmailVerification)
	secureCookies := cfg.TLS.Enabled || strings.HasPrefix(cfg.Portal.BaseURL, "https://")
	web, err := portal.NewWeb(portalSvc, secureCookies, log)
	if err != nil {
		log.Error("portal web", "err", err)
		return err
	}
	if gameStore != nil {
		// DB-backed mode: enable the Portal's save import/export (with quartz
		// balance transfer via the gem store).
		web.EnableTransfer(portal.TransferDeps{
			Accounts: accounts,
			Game:     gameStore,
			Gems:     gemStore,
			Sessions: sessionStoreOf(gameStore),
		})
	}
	shim := portal.NewShimWithMaster(portalSvc, balanceProvider, md, log)
	shim.SetRegion(cfg.Portal.CountryCode, cfg.Portal.CurrencyCode)

	root := dispatch(game, web.Routes(), shim.Routes())
	if os.Getenv("LILYPAD_ACCESS_LOG") != "" {
		root = accessLog(root, log)
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           root,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownDone := make(chan struct{})
	go func() {
		<-shutdownCtx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Error("shutdown", "err", err)
			_ = srv.Close()
		}
		close(shutdownDone)
	}()

	log.Info("lilypad listening",
		"addr", cfg.Listen, "signing", cfg.Signing.Mode, "tls", cfg.TLS.Enabled,
		"email_verification", cfg.Portal.RequireEmailVerification)
	if cfg.TLS.Enabled {
		err = srv.ListenAndServeTLS(cfg.TLS.Cert, cfg.TLS.Key)
	} else {
		err = srv.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		<-shutdownDone
		return nil
	}
	return err
}

// dispatch routes by path prefix to the three surfaces.
func dispatch(game, web, shim http.Handler) http.Handler {
	shimStripped := http.StripPrefix("/v1.0", shim)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasPrefix(p, "/api/") || p == "/healthz" || p == "/readyz":
			game.ServeHTTP(w, r)
		case strings.HasPrefix(p, "/v1.0/"):
			shimStripped.ServeHTTP(w, r)
		default:
			web.ServeHTTP(w, r)
		}
	})
}

// accessLog wraps h to log one line per request (method, path, status, size).
// Diagnostic-only; enabled by setting LILYPAD_ACCESS_LOG. Logs every request
// including ones that fall through to a 404, so unmatched client calls (e.g. an
// unimplemented /v1.0 endpoint) are visible. Sensitive headers/query fields are
// redacted and bodies are never buffered or logged by this wrapper.
func accessLog(h http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verbose := strings.HasPrefix(r.URL.Path, "/v1.0/")
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(rec, r)
		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"query", redactedQuery(r),
			"status", rec.status,
			"bytes", rec.bytes,
		}
		if verbose {
			attrs = append(attrs, "headers", headerString(r.Header))
		}
		log.Info("req", attrs...)
	})
}

// headerString flattens request headers into one log-friendly string.
func headerString(h http.Header) string {
	var b strings.Builder
	for k, vs := range h {
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		b.WriteString(k)
		b.WriteString("=")
		switch strings.ToLower(k) {
		case "authorization", "cookie", "x-signature", "x-lastsignature", "x-lilypad-session":
			b.WriteString("<REDACTED>")
		default:
			b.WriteString(strings.Join(vs, ","))
		}
	}
	return b.String()
}

func redactedQuery(r *http.Request) string {
	q := r.URL.Query()
	for key := range q {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "password") || lower == "migration_code" {
			q.Set(key, "<REDACTED>")
		}
	}
	return q.Encode()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}

// statusRecorder captures the response status and byte count for accessLog.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

func buildSigner(cfg *config.Config) (sign.Signer, error) {
	switch cfg.Signing.Mode {
	case "rsa":
		pem, err := os.ReadFile(cfg.Signing.PrivateKeyPEM)
		if err != nil {
			return nil, err
		}
		return sign.NewRSASigner(pem)
	case "noop":
		return sign.NoopSigner{}, nil
	default:
		return nil, fmt.Errorf("unknown signing mode %q", cfg.Signing.Mode)
	}
}

func buildMailer(cfg *config.Config, log *slog.Logger) portal.Mailer {
	s := cfg.Portal.SMTP
	if cfg.Portal.RequireEmailVerification && s.Host != "" {
		addr := s.Host
		port := s.Port
		if port == 0 {
			port = 25
		}
		addr = net.JoinHostPort(s.Host, strconv.Itoa(port))
		var auth smtp.Auth
		if s.User != "" {
			auth = smtp.PlainAuth("", s.User, s.Pass, s.Host)
		}
		return portal.SMTPMailer{Addr: addr, Auth: auth, From: s.From}
	}
	return portal.LogMailer{Log: log}
}

func sessionStoreOf(st store.Store) store.SessionStore {
	if ss, ok := st.(store.SessionStore); ok {
		return ss
	}
	return nil
}

// loadMaster loads master data when data_dir is configured; economy RPCs are
// disabled (not registered) when it is absent.
func loadMaster(cfg *config.Config, log *slog.Logger) (*master.Data, error) {
	if cfg.DataDir == "" {
		log.Warn("no data_dir configured: economy RPCs disabled")
		return nil, nil
	}
	md, err := master.Load(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("configured master data could not be loaded: %w", err)
	}
	log.Info("master data loaded", "files", md.FileCount())
	if md.FileCount() == 0 {
		return nil, fmt.Errorf("configured master data directory contains no files")
	}
	for _, name := range []string{"MasterReward", "MasterItem", "MasterMission", "MasterLottery", "MasterLoginBonus", "MasterLoginBonusContent"} {
		if _, ok := md.File(name); !ok {
			return nil, fmt.Errorf("configured master data is missing %s", name)
		}
	}
	return md, nil
}

// gemBalanceAdapter lets the platform shim read a player's gem balance: it
// resolves the platform XUID to the game account, then reads the gem store.
type gemBalanceAdapter struct {
	accounts account.Provider
	gems     gem.Store
}

func (a gemBalanceAdapter) GemBalance(ctx context.Context, xuid string) (free, paid int64, err error) {
	acc, err := a.accounts.GetOrCreate(ctx, xuid)
	if err != nil {
		return 0, 0, err
	}
	b, err := a.gems.Get(ctx, acc.UserID)
	if err != nil {
		return 0, 0, err
	}
	return b.Free, b.Paid, nil
}
