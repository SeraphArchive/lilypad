// Package httpapi implements the game-API HTTP surface: it decodes the wire
// codec, dispatches by route, then encodes and signs the response. Handlers are
// pinned to captured traffic shapes (see internal/protocol).
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"lilypad/internal/account"
	"lilypad/internal/clientversion"
	"lilypad/internal/codec"
	"lilypad/internal/config"
	"lilypad/internal/gem"
	"lilypad/internal/master"
	"lilypad/internal/protocol"
	"lilypad/internal/sign"
	"lilypad/internal/store"
)

// Server holds the game-API dependencies and route table.
type Server struct {
	cfg      *config.Config
	signer   sign.Signer
	accounts account.Provider
	store    store.Store             // optional; enables DeltaComm save-sync routes when non-nil
	master   *master.Data            // optional; enables economy RPC routes when non-nil
	gems     gem.Store               // optional; set when the store implements gem.Store
	sessions store.SessionStore      // optional; set when the store implements it (401 session guard)
	versions *clientversion.Registry // installed-build version from clientpatch reports
	log      *slog.Logger

	systemLocks []protocol.SystemLock
	lotteryShop []int

	// webShopProducts is the configured user_web_shop_product row set served
	// by /api/web/shop/product/list (from cfg.Constants.WebShopProducts).
	webShopProducts []map[string]any

	mux *chi.Mux
}

// New wires the router and precomputes config-derived constants. st and md
// may be nil (canned/Phase-2 builds); the DeltaComm save-sync routes register
// only with a store, and the economy RPC routes only with both a store and
// master data.
func New(cfg *config.Config, signer sign.Signer, accounts account.Provider, st store.Store, md *master.Data, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{cfg: cfg, signer: signer, accounts: accounts, store: st, master: md, log: log}
	if gs, ok := st.(gem.Store); ok {
		s.gems = gs
	}
	if ss, ok := st.(store.SessionStore); ok {
		s.sessions = ss
	}
	var gvs clientversion.GlobalStore
	if gs, ok := st.(clientversion.GlobalStore); ok {
		gvs = gs
	}
	s.versions = clientversion.New(gvs, log)

	// Config constants are loaded as generic YAML; normalize to typed shapes once.
	_ = convert(cfg.Constants.SystemLock, &s.systemLocks)
	_ = convert(cfg.Constants.LotteryShop, &s.lotteryShop)
	_ = convert(cfg.Constants.WebShopProducts, &s.webShopProducts)
	if s.systemLocks == nil {
		s.systemLocks = []protocol.SystemLock{}
	}
	if s.lotteryShop == nil {
		s.lotteryShop = []int{}
	}

	r := chi.NewRouter()
	r.Use(maxBodyMiddleware)
	r.Use(s.masterMiddleware)
	r.Use(s.requestMiddleware)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if cfg.DataDir != "" && md == nil || cfg.DB.DSN != "" && st == nil {
			http.Error(w, "required dependency unavailable", http.StatusServiceUnavailable)
			return
		}
		if p, ok := st.(interface{ Ping(context.Context) error }); ok {
			if err := p.Ping(r.Context()); err != nil {
				http.Error(w, "database unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		_, _ = io.WriteString(w, "ok")
	})
	r.Post("/api/app/start", s.handleAppStart)
	r.Post("/api/clientpatch/version", s.handleClientVersionReport)
	r.Post("/api/user/client/id", s.handleClientID)
	if st != nil {
		r.Post("/api/user/confirm", s.handleConfirm)
		r.Post("/api/user/pull", s.handlePull)
		r.Post("/api/user/push", s.handlePush)
		r.Post("/api/user/migration/prepare", s.handleMigrationPrepare)
		r.Post("/api/user/migration/id", s.handleMigrationID)
		r.Post("/api/random/setup", s.handleRandomSetup)
		// Economy RPCs that compute server-authoritative rewards from master data.
		r.Post("/api/lottery/draw", s.handleLotteryDraw)
		r.Post("/api/lottery/card/exchange", s.handleLotteryCardExchange)
		r.Post("/api/item/lottery/draw", s.handleItemLotteryDraw)
		r.Post("/api/mission/loop/receive", s.handleLoopMissionReceive)
		r.Post("/api/lottery/prepare", s.handleLotteryPrepare)
		r.Post("/api/daily/update", s.handleDailyUpdate)
		r.Post("/api/mission/receive", s.handleMissionReceive)
		r.Post("/api/gift/receive", s.handleGiftReceive)
		r.Post("/api/invite/reward/receive", s.handleInviteRewardReceive)
		r.Post("/api/stockable_regular_reward/receive", s.handleStockableRegularRewardReceive)
		r.Post("/api/web/shop/product/list", s.handleWebShopProductList)
		r.Post("/api/stamina/recover", s.handleStaminaRecover)
		r.Post("/api/spirit/recover", s.handleSpiritRecover)
		r.Post("/api/life/recover", s.handleLifeRecover)
		r.Post("/api/user/profile", s.handleUserProfile)
		r.Post("/api/live/ranking", s.handleLiveRanking)
		r.Post("/api/arcade/ranking/list", s.handleArcadeRankingList)
		r.Post("/api/user/other/arcade/result/fetch", s.handleArcadeResultFetch)
		r.Post("/api/wave_battle/ranking/list", s.handleWaveBattleRankingList)
		r.Post("/api/user/other/wave_battle/result/fetch", s.handleWaveBattleResultFetch)
		r.Post("/api/octopus/hunting/ranking/list", s.handleOctopusRankingList)
	}
	s.mux = r
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// Handler exposes the underlying router (for mounting alongside Portal/shim).
func (s *Server) Handler() http.Handler { return s.mux }

// --- handlers ---

func (s *Server) handleAppStart(w http.ResponseWriter, r *http.Request) {
	var req protocol.AppStartRequest
	if err := s.decode(r, &req); err != nil {
		s.log.Warn("app/start decode", "err", err) // lenient: proceed with zero values
	}
	assetVersion, assetHash := s.cfg.Asset.Version, s.cfg.Asset.Hash
	if rep, ok := s.versions.Resolve(r.Header.Get("x-clientpatch-id")); ok &&
		rep.AssetVersion != "" && rep.AssetHash != "" {
		assetVersion, assetHash = rep.AssetVersion, rep.AssetHash
	}
	s.respond(w, r, protocol.AppStartResponse{
		Code:          0,
		AssetVersion:  assetVersion,
		AssetHash:     assetHash,
		Tables:        []any{},
		Hashes:        []any{},
		ServerCommand: []any{},
		SystemLock:    s.systemLocks,
		LotteryShop:   s.lotteryShop,
	})
}

// clientVersionReport is the plain-JSON body clientpatch POSTs at game launch.
// It is NOT codec-encoded (clientpatch sends application/json directly), unlike
// the game's own /api/* routes.
type clientVersionReport struct {
	ClientID       string `json:"clientId"`
	ProgramVersion string `json:"programVersion"`
	AssetVersion   string `json:"assetVersion"`
	AssetHash      string `json:"assetHash"`
}

// handleClientVersionReport records a clientpatch launch report, keyed by its
// client ID, and advances the global latest. Unauthenticated, like /healthz: it
// sits on the same trust boundary as the rest of the private server.
func (s *Server) handleClientVersionReport(w http.ResponseWriter, r *http.Request) {
	var req clientVersionReport
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, `{"status":"bad request"}`, http.StatusBadRequest)
		return
	}
	if req.ClientID == "" || req.AssetVersion == "" || req.AssetHash == "" || len(req.ClientID) > 128 || len(req.AssetVersion) > 64 || len(req.ProgramVersion) > 64 || len(req.AssetHash) > 128 {
		http.Error(w, `{"status":"missing fields"}`, http.StatusBadRequest)
		return
	}
	s.versions.Report(req.ClientID, clientversion.Report{
		ProgramVersion: req.ProgramVersion,
		AssetVersion:   req.AssetVersion,
		AssetHash:      req.AssetHash,
	})
	s.log.Info("clientpatch version report",
		"client", req.ClientID,
		"program", req.ProgramVersion,
		"asset", req.AssetVersion,
		"hash", req.AssetHash,
	)
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

func (s *Server) handleClientID(w http.ResponseWriter, r *http.Request) {
	var req protocol.ClientIDRequest
	if err := s.decode(r, &req); err != nil {
		s.log.Warn("client/id decode", "err", err)
	}
	id := req.XUID
	if id == "" {
		id = r.Header.Get("x-player-id")
	}
	userID, ok := s.resolvePlayer(r.Context(), id)
	if !ok {
		s.fail(w, r, 1, "missing x_uid")
		return
	}
	if !s.recordSessionBase(r, userID) {
		s.fail(w, r, 1, "could not start session")
		return
	}
	s.respond(w, r, protocol.ClientIDResponse{
		UserID:        userID,
		ServerCommand: []any{},
		SystemLock:    s.systemLocks,
	})
}

// handleMigrationID answers POST /api/user/migration/id during the 引き継ぎ
// takeover: it returns the game userId for the migrated account (identified by the
// x-player-id XUID the client sends after the platform migration/code/verify).
// The response shape is Migration.Id : ResponsePayload { userId }, identical to
// client/id. The game's DataMigrationIdGetState reads userId and isUserExists()
// gates login on it, so a missing route (404) aborts to the title screen.
func (s *Server) handleMigrationID(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.playerID(r)
	if !ok {
		s.fail(w, r, 1, "missing x-player-id")
		return
	}
	if !s.recordSessionBase(r, userID) {
		s.fail(w, r, 1, "could not start session")
		return
	}
	s.respond(w, r, protocol.ClientIDResponse{
		UserID:        userID,
		ServerCommand: []any{},
		SystemLock:    s.systemLocks,
	})
}

// --- codec plumbing ---

func (s *Server) decode(r *http.Request, dst any) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if len(body) == 0 {
		return nil // empty / hdr-only bodies are tolerated
	}
	if err := codec.DecodeInto(body, dst); err != nil {
		return err
	}
	return nil
}

func (s *Server) respond(w http.ResponseWriter, _ *http.Request, obj any) {
	wire, err := codec.Encode(obj)
	if err != nil {
		s.log.Error("encode response", "err", err)
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	if sig, ok := s.signer.Sign(wire); ok {
		w.Header().Set("x-signature", sig)
	}
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(wire); err != nil {
		s.log.Warn("write response", "err", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, code int, msg string) {
	s.respond(w, r, protocol.FailedResponse{Code: code, Message: msg})
}

// maxRequestBody bounds the compressed request body. Codec bodies are
// gzip-compressed JSON, so this is generous headroom; the decompressed size is
// separately bounded by the codec.
const maxRequestBody = 16 << 20 // 16 MiB

// maxBodyMiddleware caps the request body size to mitigate memory-exhaustion DoS.
func maxBodyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		next.ServeHTTP(w, r)
	})
}

// convert re-encodes src (generic YAML/JSON values) into the typed dst.
func convert(src, dst any) error {
	b, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

// emptyMapAsArray mirrors the client's serializer, which renders an empty
// dictionary as a JSON array. Non-empty maps serialize as objects.
func emptyMapAsArray[T any](m map[string]T) any {
	if len(m) == 0 {
		return []any{}
	}
	return m
}
