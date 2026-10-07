package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"lilypad/internal/codec"
	"lilypad/internal/hashing"
	"lilypad/internal/store"
)

// Request locking and retry persistence must enclose both the handler and its
// mutations: cached rows alone cannot reconstruct the exact original response.
func (s *Server) requestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs, ok := s.store.(store.RequestStore)
		if !ok || !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/app/start" || r.URL.Path == "/api/clientpatch/version" || r.URL.Path == "/api/user/client/id" || r.URL.Path == "/api/user/migration/id" || r.URL.Path == "/api/user/migration/prepare" {
			next.ServeHTTP(w, r)
			return
		}
		uid, ok := s.playerID(r)
		if !ok {
			s.fail(w, r, 1, "missing x-player-id")
			return
		}
		msgid, _ := strconv.ParseInt(r.Header.Get("x-msgid"), 10, 64)
		generation, err := rs.CheckSession(r.Context(), uid, r.Header.Get("x-lilypad-session"), msgid)
		if errors.Is(err, store.ErrSession) {
			writeStaleSession(w)
			return
		}
		if err != nil {
			http.Error(w, "session unavailable", http.StatusServiceUnavailable)
			return
		}
		key := ""
		digest := ""
		if retryableRoute(r.URL.Path) {
			operation := r.Header.Get("Idempotency-Key")
			if operation == "" && msgid > 0 {
				operation = strconv.FormatInt(msgid, 10)
			}
			if len(operation) > 128 {
				s.fail(w, r, 1, "invalid operation identifier")
				return
			}
			if operation != "" {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					s.fail(w, r, 1, "request too large")
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(raw))
				decoded, err := codec.Decode(raw)
				if err != nil {
					s.fail(w, r, 1, "invalid request body")
					return
				}
				canonical, err := hashing.CanonicalJSON(decoded)
				if err != nil {
					s.fail(w, r, 1, "invalid request body")
					return
				}
				sum := sha256.Sum256(canonical)
				digest = hex.EncodeToString(sum[:])
				key = r.URL.Path + ":" + operation
				if r.URL.Path == "/api/user/push" && r.Header.Get("Idempotency-Key") == "" {
					// DCC retries recreate x-msgid, but the queued packet and its
					// baselines stay the same. Identify that packet, not a transport attempt.
					delete(decoded, "hdr")
					canonical, err = hashing.CanonicalJSON(decoded)
					if err != nil {
						s.fail(w, r, 1, "invalid push packet")
						return
					}
					sum = sha256.Sum256(canonical)
					digest = hex.EncodeToString(sum[:])
					key = r.URL.Path + ":" + digest
				}
			}
			if operation == "" {
				s.fail(w, r, 1, "missing operation identifier")
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		response, err := rs.RunRequest(ctx, uid, generation, key, digest, func(ctx context.Context) (store.Response, error) {
			recorder := &responseBuffer{header: make(http.Header)}
			next.ServeHTTP(recorder, r.WithContext(ctx))
			status := recorder.status
			if status == 0 {
				status = http.StatusOK
			}
			return store.Response{Status: status, Header: recorder.Header().Clone(), Body: recorder.body.Bytes()}, nil
		})
		if errors.Is(err, store.ErrSession) {
			writeStaleSession(w)
			return
		}
		if errors.Is(err, store.ErrRequestConflict) {
			s.fail(w, r, 1, "operation identifier already used")
			return
		}
		if err != nil {
			s.log.Error("request transaction", "route", r.URL.Path, "err", err)
			http.Error(w, "request failed", http.StatusServiceUnavailable)
			return
		}
		for k, v := range response.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
	})
}
func retryableRoute(path string) bool {
	switch path {
	case "/api/user/push", "/api/lottery/draw", "/api/item/lottery/draw", "/api/lottery/card/exchange", "/api/stamina/recover", "/api/spirit/recover", "/api/life/recover":
		return true
	}
	return false
}
func writeStaleSession(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	w.WriteHeader(http.StatusUnauthorized)
}

type responseBuffer struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (b *responseBuffer) Header() http.Header { return b.header }
func (b *responseBuffer) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}
func (b *responseBuffer) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}
