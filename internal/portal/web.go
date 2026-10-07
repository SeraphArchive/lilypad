package portal

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

const sessionCookie = "lilypad_session"

// Web serves the Portal HTML UI.
type Web struct {
	svc       *Service
	log       *slog.Logger
	templates map[string]*template.Template
	sessions  *sessionStore
	secure    bool // set the Secure flag on session cookies (TLS deployments)

	transfer *TransferDeps // optional save import/export (nil in dev mode)
}

// NewWeb builds the Portal web handler. secure controls the Secure cookie flag
// (enable behind TLS).
func NewWeb(svc *Service, secure bool, log *slog.Logger) (*Web, error) {
	if log == nil {
		log = slog.Default()
	}
	pages := []string{"login", "register", "register_success", "account", "takeover", "transfer"}
	templates := map[string]*template.Template{}
	for _, p := range pages {
		t, err := template.New("layout.html").ParseFS(templatesFS, "templates/layout.html", "templates/"+p+".html")
		if err != nil {
			return nil, err
		}
		templates[p] = t
	}
	return &Web{svc: svc, log: log, templates: templates, sessions: newSessionStore(), secure: secure}, nil
}

// Routes returns the Portal router.
func (web *Web) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(http.NewCrossOriginProtection().Handler)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			limit := int64(1 << 20)
			if r.URL.Path == "/transfer/import" {
				limit = maxArchiveBytes + (1 << 20)
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	})
	sub, _ := fs.Sub(staticFS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		if web.current(r) != nil {
			http.Redirect(w, r, "/account", http.StatusSeeOther)
		} else {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		}
	})
	r.Get("/login", web.getLogin)
	r.Post("/login", web.postLogin)
	r.Get("/register", web.getRegister)
	r.Post("/register", web.postRegister)
	r.Get("/verify", web.getVerify)
	r.Post("/verify/resend", web.postResendVerification)
	r.Post("/logout", web.postLogout)
	r.Get("/account", web.requireAuth(web.getAccount))
	r.Get("/takeover", web.requireAuth(web.getTakeover))
	r.Post("/takeover/reset", web.requireAuth(web.postTakeoverReset))
	r.Get("/transfer", web.requireAuth(web.requireTransfer(web.getTransfer)))
	r.Get("/transfer/export", web.requireAuth(web.requireTransfer(web.getExport)))
	r.Post("/transfer/import", web.requireAuth(web.requireTransfer(web.postImport)))
	return r
}

// requireTransfer 404s the save-transfer routes when the feature is disabled
// (no save store wired, e.g. in-memory dev mode).
func (web *Web) requireTransfer(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if web.transfer == nil {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}

// --- view model ---

type viewData struct {
	Nav                  string
	Authed               bool
	CSRF                 string
	Flash                string
	Error                string
	Email                string
	Account              Credential
	MigrationCode        string
	MigrationPassword    string
	VerificationRequired bool
	TransferEnabled      bool
	Save                 *saveSummary
}

func (web *Web) render(w http.ResponseWriter, r *http.Request, page string, vd viewData) {
	if vd.Save == nil {
		vd.Save = &saveSummary{}
	}
	if sess := web.current(r); sess != nil {
		vd.Authed = true
		vd.CSRF = sess.csrf
	}
	vd.TransferEnabled = web.transfer != nil
	t, ok := web.templates[page]
	if !ok {
		http.Error(w, "unknown page", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout.html", vd); err != nil {
		web.log.Error("render", "page", page, "err", err)
		http.Error(w, "could not render page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

// --- handlers ---

func (web *Web) getLogin(w http.ResponseWriter, r *http.Request) {
	web.render(w, r, "login", viewData{Nav: "login"})
}

func (web *Web) postLogin(w http.ResponseWriter, r *http.Request) {
	if !web.checkCSRF(r) {
		web.render(w, r, "login", viewData{Nav: "login", Error: "invalid session, try again"})
		return
	}
	email, password := r.FormValue("email"), r.FormValue("password")
	cred, err := web.svc.Authenticate(r.Context(), email, password)
	if err != nil {
		msg := "invalid email or password"
		if errors.Is(err, ErrNotVerified) {
			msg = "please verify your email before logging in"
		}
		web.render(w, r, "login", viewData{Nav: "login", Email: email, Error: msg})
		return
	}
	web.startSession(w, cred.Email)
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

func (web *Web) getRegister(w http.ResponseWriter, r *http.Request) {
	web.render(w, r, "register", viewData{Nav: "register"})
}

func (web *Web) postRegister(w http.ResponseWriter, r *http.Request) {
	if !web.checkCSRF(r) {
		web.render(w, r, "register", viewData{Nav: "register", Error: "invalid session, try again"})
		return
	}
	email, password := r.FormValue("email"), r.FormValue("password")
	res, err := web.svc.Register(r.Context(), email, password)
	if err != nil {
		msg := err.Error()
		if errors.Is(err, ErrEmailTaken) {
			msg = "that email is already registered"
		}
		web.render(w, r, "register", viewData{Nav: "register", Email: email, Error: msg})
		return
	}
	// Establish a session so the success page navigation works.
	if !web.svc.requireVerification {
		web.startSession(w, normalizeEmail(email))
	}
	flash := ""
	if res.VerificationDeliveryFailed {
		flash = "Account created. Email could not be delivered; use the verification resend form on the login page."
	}
	web.render(w, r, "register_success", viewData{
		Flash:                flash,
		Nav:                  "account",
		Email:                normalizeEmail(email),
		MigrationCode:        res.MigrationCode,
		MigrationPassword:    res.MigrationPassword,
		VerificationRequired: res.VerificationLink != "",
	})
}

func (web *Web) getVerify(w http.ResponseWriter, r *http.Request) {
	err := web.svc.VerifyEmail(r.Context(), r.URL.Query().Get("token"))
	vd := viewData{Nav: "login", Flash: "Email verified — you can now log in."}
	if err != nil {
		vd.Flash = ""
		vd.Error = "verification link is invalid or expired"
	}
	web.render(w, r, "login", vd)
}

func (web *Web) postLogout(w http.ResponseWriter, r *http.Request) {
	if web.checkCSRF(r) {
		web.endSession(w, r)
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (web *Web) getAccount(w http.ResponseWriter, r *http.Request) {
	sess := web.current(r)
	if sess == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	acc, err := web.svc.Account(r.Context(), sess.email)
	if err != nil {
		web.endSession(w, r)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	web.render(w, r, "account", viewData{Nav: "account", Account: acc})
}

func (web *Web) getTakeover(w http.ResponseWriter, r *http.Request) {
	sess := web.current(r)
	if sess == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	acc, err := web.svc.Account(r.Context(), sess.email)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	web.render(w, r, "takeover", viewData{Nav: "takeover", Account: acc})
}

func (web *Web) postTakeoverReset(w http.ResponseWriter, r *http.Request) {
	sess := web.current(r)
	if sess == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	acc, err := web.svc.Account(r.Context(), sess.email)
	if err != nil {
		web.endSession(w, r)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !web.checkCSRF(r) {
		web.render(w, r, "takeover", viewData{Nav: "takeover", Account: acc, Error: "invalid session, try again"})
		return
	}
	newPw, err := web.svc.ResetMigrationPassword(r.Context(), sess.email)
	if err != nil {
		web.render(w, r, "takeover", viewData{Nav: "takeover", Account: acc, Error: "could not reset takeover password"})
		return
	}
	web.render(w, r, "takeover", viewData{
		Nav:               "takeover",
		Account:           acc,
		MigrationPassword: newPw,
		Flash:             "New takeover password generated. Save it now — it will not be shown again.",
	})
}

func (web *Web) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := web.current(r)
		if sess == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if web.svc.requireVerification {
			cred, err := web.svc.Account(r.Context(), sess.email)
			if err != nil || !cred.EmailVerified {
				web.endSession(w, r)
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), authenticatedSessionKey{}, sess)))
	}
}

// --- sessions + CSRF ---

type session struct {
	email   string
	csrf    string
	expires time.Time
}

type sessionStore struct {
	mu          sync.Mutex
	m           map[string]session
	lastCleanup time.Time
}

func newSessionStore() *sessionStore { return &sessionStore{m: map[string]session{}} }

func (web *Web) startSession(w http.ResponseWriter, email string) {
	id := GenerateToken()
	web.sessions.mu.Lock()
	web.sessions.prune(time.Now())
	if len(web.sessions.m) >= 4096 {
		var oldestID string
		var oldest time.Time
		for key, s := range web.sessions.m {
			if oldestID == "" || s.expires.Before(oldest) {
				oldestID, oldest = key, s.expires
			}
		}
		delete(web.sessions.m, oldestID)
	}
	web.sessions.m[id] = session{email: email, csrf: GenerateToken(), expires: time.Now().Add(24 * time.Hour)}
	web.sessions.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: id, Path: "/",
		HttpOnly: true, Secure: web.secure, SameSite: http.SameSiteLaxMode, MaxAge: 86400,
	})
}

func (web *Web) current(r *http.Request) *session {
	if sess, ok := r.Context().Value(authenticatedSessionKey{}).(*session); ok {
		return sess
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	web.sessions.mu.Lock()
	defer web.sessions.mu.Unlock()
	if time.Since(web.sessions.lastCleanup) > time.Minute {
		web.sessions.prune(time.Now())
	}
	sess, ok := web.sessions.m[c.Value]
	if !ok || time.Now().After(sess.expires) {
		if ok {
			delete(web.sessions.m, c.Value)
		}
		return nil
	}
	return &sess
}

func (web *Web) endSession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		web.sessions.mu.Lock()
		delete(web.sessions.m, c.Value)
		web.sessions.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
}

func (web *Web) checkCSRF(r *http.Request) bool {
	sess := web.current(r)
	if sess == nil {
		// No session yet (login/register first POST): accept, the action itself
		// is the authentication boundary. Mutating authed actions require a match.
		return true
	}
	return r.FormValue("csrf") == sess.csrf
}

type authenticatedSessionKey struct{}

func (s *sessionStore) prune(now time.Time) {
	for id, sess := range s.m {
		if !now.Before(sess.expires) {
			delete(s.m, id)
		}
	}
	s.lastCleanup = now
}

func (web *Web) postResendVerification(w http.ResponseWriter, r *http.Request) {
	if !web.checkCSRF(r) {
		web.render(w, r, "login", viewData{Error: "invalid session"})
		return
	}
	err := web.svc.ResendVerification(r.Context(), r.FormValue("email"), r.FormValue("password"))
	if err != nil {
		web.render(w, r, "login", viewData{Error: "Could not send verification. Check your credentials and try again later."})
		return
	}
	web.render(w, r, "login", viewData{Flash: "Verification email sent. Check your inbox."})
}
