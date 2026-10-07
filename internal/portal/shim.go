package portal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"lilypad/internal/gem"
	"lilypad/internal/master"
)

// Shim implements the subset of the Gree GameLib platform API
// (gl-payment.gree-apps.net /v1.0/*) the client needs. It returns plain JSON;
// inbound OAuth signatures are not verified (the native client does not verify
// the response signature, so no consumer key is required).
type Shim struct {
	svc      *Service
	log      *slog.Logger
	balances BalanceProvider // optional; nil -> report 0/0
	master   *master.Data    // optional; nil -> default quartz ladder

	// Country/currency reported by payment/purchase/steam/userinfo. The real
	// platform derives them from the Steam account's store region; a private
	// server has no such lookup, so they are configured.
	countryCode  string
	currencyCode string
}

// XAppID is the game's platform application id, returned by auth/x_uid. The
// client only requires it non-empty, but serving the real value keeps the
// response faithful.
const XAppID = "100000131"

// BalanceProvider supplies a player's gem balance to the platform shim,
// resolving the platform XUID to the game account internally. nil is tolerated
// (canned/no-DB builds) — the shim then reports 0/0.
type BalanceProvider interface {
	GemBalance(ctx context.Context, xuid string) (free, paid int64, err error)
}

// NewShim builds the platform shim.
func NewShim(svc *Service, balance BalanceProvider, log *slog.Logger) *Shim {
	return NewShimWithMaster(svc, balance, nil, log)
}

// NewShimWithMaster is NewShim plus optional master data for productlist.
func NewShimWithMaster(svc *Service, balance BalanceProvider, md *master.Data, log *slog.Logger) *Shim {
	if log == nil {
		log = slog.Default()
	}
	return &Shim{svc: svc, log: log, balances: balance, master: md,
		countryCode: "CN", currencyCode: "CNY"}
}

// SetRegion overrides the country/currency reported by steam/userinfo.
func (sh *Shim) SetRegion(countryCode, currencyCode string) {
	if countryCode != "" {
		sh.countryCode = countryCode
	}
	if currencyCode != "" {
		sh.currencyCode = currencyCode
	}
}

// Routes returns the shim router (mount under /v1.0).
func (sh *Shim) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			next.ServeHTTP(w, r)
		})
	})
	r.Post("/auth/authorize", sh.authorize)
	r.Get("/auth/now", sh.authNow)
	r.Get("/auth/x_uid", sh.authXuid)
	r.Post("/auth/x_uid", sh.authXuid) // registerXuid: same as GET, never mint
	r.Post("/payment/purchase/steam/userinfo", sh.steamUserinfo)
	r.Post("/migration/code/verify", sh.migrationVerify)
	r.Post("/migration", sh.migration)
	r.Get("/payment/balance", sh.balance)
	r.Get("/payment/productlist", sh.productList)
	r.Get("/payment/purchase/alert/setting", sh.alertSetting)
	r.Get("/moderate/keywordlist", sh.keywordList)
	r.Get("/linked/active/info", sh.linkedActiveInfo)
	r.Post("/linked/active/update", sh.linkedActiveUpdate)
	return r
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON document")
	}
	return nil
}

// gemEntry builds the three string-encoded gem-balance fields the client expects
// (the gacha gem is the Gree PAYMENT balance, not a game-API currency).
func gemEntry(free, paid int64) map[string]any {
	return map[string]any{
		"balance_charge_gem": strconv.FormatInt(paid, 10),
		"balance_free_gem":   strconv.FormatInt(free, 10),
		"balance_total_gem":  strconv.FormatInt(free+paid, 10),
	}
}

// gemBalanceFor reads the stored balance for a resolved XUID. Development mode
// without a provider has no wallet; configured providers must return valid data.
func (sh *Shim) gemBalanceFor(ctx context.Context, xuid string) (free, paid int64, err error) {
	if sh.balances == nil || xuid == "" {
		return 0, 0, nil
	}
	f, p, err := sh.balances.GemBalance(ctx, xuid)
	if err == nil {
		err = (gem.Balance{Free: f, Paid: p}).Validate()
	}
	if err != nil {
		sh.log.Warn("gem balance read failed", "xuid", xuid, "err", err)
		return 0, 0, err
	}
	return f, p, nil
}

// xuidFromRequest resolves the XUID for this client: the persisted requestor
// binding from 引き継ぎ, or the XUID decoded from a "lilypad-<xuid>" requestor.
//
// The prefix fallback exists because migration/code/verify returns
// src_uuid = "lilypad-"+xuid and the client adopts that as its
// xoauth_requestor_id, so the binding (keyed by the pre-migration requestor)
// misses. Only a suffix shaped as a platform XUID is accepted. This protocol
// compatibility path trusts the client identity; it is not authentication.
func (sh *Shim) xuidFromRequest(r *http.Request) (string, error) {
	rid := requestorID(r)
	if rid == "" {
		return "", nil
	}
	if sh.svc != nil {
		xuid, err := sh.svc.XUIDForRequestor(r.Context(), rid)
		if err == nil {
			return xuid, nil
		}
		if !errors.Is(err, ErrNotFound) {
			sh.log.Warn("requestor binding lookup failed", "requestor", rid, "err", err)
			return "", err
		}
	}
	if s, ok := strings.CutPrefix(rid, "lilypad-"); ok && isPlatformXUID(s) {
		return s, nil
	}
	return "", nil
}

// isPlatformXUID reports whether s has the shape of a Portal-minted XUID:
// 32 lowercase hex characters (see GenerateXUID). Official XUIDs are the same
// shape, so both are accepted here.
func isPlatformXUID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

func (sh *Shim) steamUserinfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"result": "OK",
		"entry": map[string]any{
			"country_code": sh.countryCode, "currency_code": sh.currencyCode,
			"currency_matching_type": "Fixed",
		},
	})
}

func (sh *Shim) migrationVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MigrationCode     string `json:"migration_code"`
		MigrationPassword string `json:"migration_password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, map[string]any{"result": "NG", "error": "invalid migration request"})
		return
	}
	if sh.svc == nil {
		writeJSON(w, map[string]any{"result": "NG", "error": "migration unavailable"})
		return
	}
	xuid, err := sh.svc.ResolveMigration(r.Context(), req.MigrationCode, req.MigrationPassword)
	if err != nil {
		sh.log.Warn("shim migration verify failed", "err", err)
		writeJSON(w, map[string]any{"result": "NG", "error": "invalid migration code or password"})
		return
	}
	// Bind the old requestor when present, or the source UUID that a fresh
	// title-screen client will adopt. Native migration allows absent dst_uuid:
	// a preexisting device identity is not a prerequisite for credential checks.
	free, paid, err := sh.gemBalanceFor(r.Context(), xuid)
	if err != nil {
		writeJSON(w, map[string]any{"result": "NG", "error": "balance unavailable"})
		return
	}
	if err := sh.bindRequestor(r, xuid); err != nil {
		writeJSON(w, map[string]any{"result": "NG", "error": "could not bind account"})
		return
	}
	entry := gemEntry(free, paid)
	// uuid form is consumer_key-prefixed + per-user hex; the game only consumes
	// src_x_uid, so a deterministic stand-in uuid is sufficient.
	writeJSON(w, map[string]any{
		"result":             "OK",
		"src_uuid":           "lilypad-" + xuid,
		"src_x_uid":          xuid,
		"migration_token":    GenerateToken(),
		"balance_charge_gem": entry["balance_charge_gem"],
		"balance_free_gem":   entry["balance_free_gem"],
		"balance_total_gem":  entry["balance_total_gem"],
	})
}

func (sh *Shim) migration(w http.ResponseWriter, r *http.Request) {
	// Device binding is recorded as a no-op in v1.
	writeJSON(w, map[string]any{"result": "OK"})
}

// balance reports the player's gem balance (the Gree PAYMENT balance the gacha
// reads). The values come from the stored gem balance via the BalanceProvider;
// a nil provider or unknown player reports 0/0.
func (sh *Shim) balance(w http.ResponseWriter, r *http.Request) {
	xuid, err := sh.xuidFromRequest(r)
	if err != nil {
		writeJSON(w, map[string]any{"result": "NG", "error": "account lookup unavailable"})
		return
	}
	free, paid, err := sh.gemBalanceFor(r.Context(), xuid)
	if err != nil {
		writeJSON(w, map[string]any{"result": "NG", "error": "balance unavailable"})
		return
	}
	writeJSON(w, map[string]any{"result": "OK", "entry": gemEntry(free, paid)})
}

func (sh *Shim) productList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"result": "OK",
		"entry":  map[string]any{"products": sh.master.ProductList(time.Now().Unix()), "welcome": "0"},
	})
}

func (sh *Shim) alertSetting(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"result": "OK",
		"entry":  map[string]any{"purchase_alert": true, "threshold_amount": 100000},
	})
}

func (sh *Shim) keywordList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"result": "OK", "entry": map[string]any{"timestamp": "0", "keywords": []any{}}})
}

// authNow handles GET /v1.0/auth/now. Despite the name this is NOT a login step:
// it is the native AntiCheat module's server-time probe (60s/1800s timer, driven
// on gamelib_message_loop_work on the MAIN thread). The official success body is
// {"result":"OK","t":<unix seconds as a JSON number>}. t MUST be a number — a
// string (or any other type) throws json_exception on the main thread and
// std::terminate's the client. A 404 is a tolerated probe failure, but a typed
// 200 lets the 60s timer succeed instead of sitting on the INT64 "never" sentinel.
func (sh *Shim) authNow(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"result": "OK", "t": time.Now().Unix()})
}

// linkedActiveInfo / linkedActiveUpdate are the GameLib "last active device"
// pair fired on every home-entry. Official bodies are
// {"result":"OK","model":"<device>","activeTime":"<RFC3339 +0900>"}. We don't
// persist cross-device presence in v1 — both routes echo the current device
// (from X-GREE-GAMELIB) and now, which is enough for the client to treat the
// probe as success instead of a 404.
func (sh *Shim) linkedActiveInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, sh.linkedActiveBody(r))
}

func (sh *Shim) linkedActiveUpdate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, sh.linkedActiveBody(r))
}

func (sh *Shim) linkedActiveBody(r *http.Request) map[string]any {
	model := greeGamelibValue(r.Header.Get("X-GREE-GAMELIB"), "model")
	if model == "" {
		model = "Windows"
	}
	return map[string]any{
		"result":     "OK",
		"model":      model,
		"activeTime": time.Now().In(time.FixedZone("JST", 9*3600)).Format("2006-01-02T15:04:05-0700"),
	}
}

// greeGamelibValue pulls one key from the url-encoded X-GREE-GAMELIB header
// (appLanguage=…&model=Windows&…).
func greeGamelibValue(header, key string) string {
	if header == "" {
		return ""
	}
	vals, err := url.ParseQuery(header)
	if err != nil {
		return ""
	}
	return vals.Get(key)
}

// authXuid answers GET/POST /v1.0/auth/x_uid (queryXuid and registerXuid). It
// returns the XUID bound to this client's xoauth_requestor_id by a prior
// 引き継ぎ. Without a binding it returns NG (strict 引き継ぎ-first: the player
// must take over an account first). Both flat and entry-wrapped for parser
// robustness; x_app_id is the fixed platform app id (XAppID).
func (sh *Shim) authXuid(w http.ResponseWriter, r *http.Request) {
	rid := requestorID(r)
	xuid, err := sh.xuidFromRequest(r)
	if err != nil {
		writeJSON(w, map[string]any{"result": "NG", "error": "account lookup unavailable"})
		return
	}
	if xuid == "" {
		sh.log.Warn("auth/x_uid: no XUID bound for requestor; complete 引き継ぎ first", "requestor", rid)
		writeJSON(w, map[string]any{"result": "NG", "error": "no account bound; complete migration"})
		return
	}
	sh.log.Info("auth/x_uid: returning bound XUID", "requestor", rid, "x_uid", xuid)
	writeJSON(w, map[string]any{
		"result": "OK", "x_uid": xuid, "x_app_id": XAppID,
		"entry": map[string]any{"x_uid": xuid, "x_app_id": XAppID},
	})
}

// bindRequestor is called only after successful takeover credential and wallet
// checks. It persists the identity used by later auth/x_uid requests, even when
// the SDK has not yet authorized a destination device.
func (sh *Shim) bindRequestor(r *http.Request, xuid string) error {
	rid := requestorID(r)
	if rid == "" {
		// Keep this identical to migrationVerify's src_uuid response. Do not
		// mint a different anonymous UUID that the client will never receive.
		rid = "lilypad-" + xuid
	}
	if sh.svc == nil {
		sh.log.Warn("migration verify ok but shim has no service to persist requestor binding", "requestor", rid)
		return fmt.Errorf("migration unavailable")
	}
	if old, err := sh.svc.XUIDForRequestor(r.Context(), rid); err == nil && old != "" && old != xuid {
		sh.log.Warn("requestor rebound to a different XUID (device takeover)",
			"requestor", rid, "from", old, "to", xuid)
	}
	if err := sh.svc.BindRequestor(r.Context(), rid, xuid); err != nil {
		sh.log.Warn("failed to persist requestor -> XUID binding", "requestor", rid, "x_uid", xuid, "err", err)
		return err
	}
	sh.log.Info("bound requestor -> XUID via 引き継ぎ", "requestor", rid, "x_uid", xuid)
	return nil
}

// requestorID extracts the stable per-user handle (xoauth_requestor_id) from the
// request's OAuth Authorization header.
func requestorID(r *http.Request) string {
	return oauthParam(r.Header.Get("Authorization"), "xoauth_requestor_id")
}

// oauthParam pulls one parameter value from an OAuth Authorization header of the
// form: OAuth k1="v1", k2="v2", ... Values here (requestor_id hex, consumer key
// digits) are not percent-escaped, so no unescaping is needed.
func oauthParam(authHeader, key string) string {
	_, after, found := strings.Cut(authHeader, key+`="`)
	if !found {
		return ""
	}
	val, _, _ := strings.Cut(after, `"`)
	return val
}
