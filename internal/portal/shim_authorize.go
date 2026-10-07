package portal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

func (sh *Shim) authorize(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceID string `json:"device_id"`
		Token    string `json:"token"` // SDK-generated public key, not a player identity
		IDToken  string `json:"id_token"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.IDToken) == "" {
		writeJSON(w, map[string]any{"result": "NG", "error": "invalid authorization request"})
		return
	}

	// The native SDK requires a root-level, nonempty uuid before it saves its
	// UUID and key pair. An OK-only response leaves fresh auth storage empty.
	// Reauthorization must retain the established requestor, including the
	// src_uuid adopted after migration, so its durable binding remains usable.
	uuid := requestorID(r)
	if strings.TrimSpace(uuid) == "" {
		if strings.TrimSpace(req.DeviceID) == "" || strings.TrimSpace(req.Token) == "" {
			writeJSON(w, map[string]any{"result": "NG", "error": "invalid authorization request"})
			return
		}
		// Frame the fields to avoid concatenation collisions. A refreshed Steam
		// ticket does not change installation identity; a distinct public key
		// separates two isolated profiles on the same physical device. This is
		// deterministic across retries/restarts and needs no pending-account row.
		identity, _ := json.Marshal([]string{"lilypad-device-auth-v1", XAppID, req.DeviceID, req.Token})
		digest := sha256.Sum256(identity)
		// Deliberately cannot match the lilypad-<32-hex-player-XUID> fallback.
		uuid = "lilypad-device-" + hex.EncodeToString(digest[:16])
	}
	// Existing-identity SDK requests contain only id_token. Their device/key
	// pair is already persisted locally and must not be required or replaced.
	// As before, this compatibility shim does not verify Steam tickets or OAuth
	// signatures. Creating a device identity grants no account: migration still
	// requires valid takeover credentials and persists the requestor binding.
	writeJSON(w, map[string]any{"result": "OK", "uuid": uuid})
}
