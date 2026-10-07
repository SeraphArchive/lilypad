package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"lilypad/internal/deltacomm"
	"lilypad/internal/store"
)

// pullRequest is POST /api/user/pull.
type pullRequest struct {
	Hdr    string   `json:"hdr"`
	Tables []string `json:"tables"`
}

// pushRequest is POST /api/user/push. Logs are accepted but purely
// informational: the server never credits currencies from them — in
// particular user_reward_grant_log._giftNum records client-side gem-reward
// grants that the server itself pays out later at gift/receive (or
// mission/receive), so crediting them here would double-grant (and would be
// client-spoofable besides).
type pushRequest struct {
	Hdr       string                     `json:"hdr"`
	Trigger   string                     `json:"trigger"`
	SavedTime int64                      `json:"savedTime"`
	Deltas    deltacomm.Deltas           `json:"deltas"`
	Logs      map[string][]deltacomm.Row `json:"logs"`
	Hashes    map[string]string          `json:"hashes"` // client's pre-delta baselines
}

// playerID resolves the requesting player's userId from the x-player-id header.
func (s *Server) playerID(r *http.Request) (int64, bool) {
	return s.resolvePlayer(r.Context(), r.Header.Get("x-player-id"))
}

// playerIDChecked is playerID plus the concurrent-login guard: once a newer
// session has been established for the account (the login routes record its
// base message id), requests from older sessions — the x-msgid counter is
// initialized to the current unix time per process, so an old process's ids
// stay below the new session's base — are rejected with a bare 401 (the
// official "logged in on another device" response is an edge-style 401 with an
// empty text/html body, not a codec envelope).
func (s *Server) playerIDChecked(w http.ResponseWriter, r *http.Request) (int64, bool) {
	userID, ok := s.playerID(r)
	if !ok {
		s.fail(w, r, 1, "missing x-player-id")
		return 0, false
	}
	if s.sessions == nil {
		return userID, true
	}
	if rs, ok := s.store.(store.RequestStore); ok {
		msgid, _ := strconv.ParseInt(r.Header.Get("x-msgid"), 10, 64)
		_, err := rs.CheckSession(r.Context(), userID, r.Header.Get("x-lilypad-session"), msgid)
		if errors.Is(err, store.ErrSession) {
			writeStaleSession(w)
			return 0, false
		}
		if err != nil {
			http.Error(w, "session unavailable", http.StatusServiceUnavailable)
			return 0, false
		}
		return userID, true
	}
	msgid, err := strconv.ParseInt(r.Header.Get("x-msgid"), 10, 64)
	if err != nil {
		return userID, true // no counter on this request: nothing to check
	}
	base, err := s.sessions.SessionBase(r.Context(), userID)
	if err != nil {
		s.log.Warn("session base read failed", "user", userID, "err", err)
		return userID, true // fail open: a missing row must not lock players out
	}
	if base > 0 && msgid < base {
		s.log.Info("stale session rejected", "user", userID, "msgid", msgid, "base", base)
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.WriteHeader(http.StatusUnauthorized)
		return 0, false
	}
	return userID, true
}

// recordSessionBase stamps the account's session base from the request's
// x-msgid at session-establishing routes (user/client/id, user/migration/id).
func (s *Server) recordSessionBase(r *http.Request, userID int64) bool {
	if s.sessions == nil {
		return true
	}
	msgid, err := strconv.ParseInt(r.Header.Get("x-msgid"), 10, 64)
	if rs, ok := s.store.(store.RequestStore); ok {
		if len(r.Header.Get("x-lilypad-session")) > 128 {
			return false
		}
		if err != nil {
			msgid = 0
		}
		if err := rs.BeginSession(r.Context(), userID, r.Header.Get("x-lilypad-session"), msgid); err != nil {
			s.log.Error("session start failed", "err", err)
			return false
		}
		return true
	}
	if err != nil || msgid <= 0 {
		return true
	}
	if err := s.sessions.SetSessionBase(r.Context(), userID, msgid); err != nil {
		s.log.Warn("session base write failed", "user", userID, "err", err)
		return false
	}
	return true
}

// resolvePlayer maps an identity token (the x-player-id header, or the client/id
// body x_uid) to a game userId.
//
// Official wire (and LilyPad after prologue SetXUID):
//   - first contact (user/client/id, user/migration/id): token = 32-hex platform XUID
//   - every later /api call: token = decimal game userId (PlayerPrefs "xuid")
//
// Those two strings are NOT equal — official userId is a 16-digit snowflake, XUID
// is 32-hex. GetOrCreate must only run for the hex XUID. A decimal token that
// does not already exist as accounts.user_id is a stale/foreign userId, NOT a
// new platform identity. Minting an account keyed by that decimal string was
// the phantom-account bug: progress landed on a new user_id while client/id
// (body x_uid = hex) kept returning the original, which the client surfaces as
// "reset progress" / an unexpected UID change. UserExists errors fail closed
// for the same reason — falling back to GetOrCreate would mint the phantom.
func (s *Server) resolvePlayer(ctx context.Context, id string) (int64, bool) {
	if id == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(id, 10, 64); err == nil {
		if s.store == nil {
			s.log.Warn("resolvePlayer: numeric token with no store, refusing to mint", "id", id)
			return 0, false
		}
		ok, dbErr := s.store.UserExists(ctx, n)
		if dbErr != nil {
			s.log.Error("resolvePlayer: UserExists query failed, not minting",
				"id", id, "err", dbErr)
			return 0, false
		}
		if ok {
			return n, true
		}
		s.log.Warn("resolvePlayer: unknown numeric userId, not minting a phantom", "id", id)
		return 0, false
	}
	acc, err := s.accounts.GetOrCreate(ctx, id)
	if err != nil {
		s.log.Error("resolve player", "err", err)
		return 0, false
	}
	return acc.UserID, true
}

func (s *Server) handleConfirm(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.playerIDChecked(w, r)
	if !ok {
		return
	}
	hashes, err := s.store.AllHashes(r.Context(), userID)
	if err != nil {
		s.log.Error("confirm", "err", err)
		s.fail(w, r, 1, "confirm failed")
		return
	}
	for name := range hashes {
		if deltacomm.HashExcluded(name) {
			delete(hashes, name)
		}
	}
	s.respond(w, r, s.envelope(map[string]any{"hashes": emptyMapAsArray(hashes)}))
}

func (s *Server) handlePull(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.playerIDChecked(w, r)
	if !ok {
		return
	}
	var req pullRequest
	if err := s.decode(r, &req); err != nil {
		s.fail(w, r, 1, "bad pull request")
		return
	}
	tables, err := s.store.GetTables(r.Context(), userID, req.Tables)
	if err != nil {
		s.log.Error("pull", "err", err)
		s.fail(w, r, 1, "pull failed")
		return
	}
	storedHashes, err := s.store.AllHashes(r.Context(), userID)
	if err != nil {
		s.fail(w, r, 1, "pull hash read failed")
		return
	}
	hashes := make(map[string]string, len(tables))
	for name := range tables {
		// A never-created table has the empty baseline. Tombstones retain their
		// database token; a content hash cannot authorize a subsequent push.
		hashes[name] = storedHashes[name]
	}
	s.respond(w, r, s.envelope(map[string]any{
		"tables": emptyMapAsArray(tables),
		"hashes": emptyMapAsArray(hashes),
	}))
}

func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.playerIDChecked(w, r)
	if !ok {
		return
	}
	var req pushRequest
	if err := s.decode(r, &req); err != nil {
		s.fail(w, r, 1, "bad push request")
		return
	}
	// Client writes always require the version they read. nil is reserved for
	// trusted server mutations, never an option controlled by configuration.
	baselines := req.Hashes
	if baselines == nil {
		baselines = map[string]string{}
	}
	protected, supported := s.store.(store.ClientSaveStore)
	if !supported {
		s.fail(w, r, 1, "protected save storage unavailable")
		return
	}
	hashes, err := protected.ApplyClientDeltas(r.Context(), userID, req.Deltas, baselines, req.SavedTime)
	if err != nil {
		if archive, ok := s.store.(store.RejectedSaveStore); ok {
			if archiveErr := archive.RecordRejectedSave(r.Context(), userID, map[string]any{"deltas": req.Deltas, "hashes": req.Hashes, "savedTime": req.SavedTime, "trigger": req.Trigger, "reason": err.Error()}); archiveErr != nil {
				http.Error(w, "save recovery record unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		if errors.Is(err, store.ErrConcurrency) {
			s.log.Warn("push concurrency", "user", userID, "err", err)
			s.fail(w, r, 2, "save lock conflict")
			return
		}
		s.log.Error("push", "err", err)
		s.fail(w, r, 1, "push failed")
		return
	}
	s.respond(w, r, s.envelope(map[string]any{"hashes": emptyMapAsArray(hashes)}))
}
