// Package protocol holds the typed request/response shapes for LilyPad's game
// API. Response shapes are pinned to captured traffic: they are per-route, not
// a single uniform envelope (e.g. user/client/id carries no "code" field).
package protocol

// SystemLock is a maintenance/region lock window echoed on several routes.
type SystemLock struct {
	ID       string `json:"id"`
	LockType int    `json:"lockType"`
	Region   int    `json:"region"`
	OpenedAt int64  `json:"openedAt"`
	ClosedAt int64  `json:"closedAt"`
}

// --- requests ---

// AppStartRequest is POST /api/app/start.
type AppStartRequest struct {
	Hdr      string `json:"hdr"`
	Region   string `json:"region"`
	Country  string `json:"country"`
	Language string `json:"language"`
}

// ClientIDRequest is POST /api/user/client/id.
type ClientIDRequest struct {
	Hdr  string `json:"hdr"`
	XUID string `json:"x_uid"`
}

// --- responses ---

// AppStartResponse mirrors the captured /api/app/start response. Tables and
// Hashes are emitted as empty arrays here (matching the capture); the
// save-sync routes use object-shaped tables/hashes instead.
type AppStartResponse struct {
	Code          int          `json:"code"`
	AssetVersion  string       `json:"assetVersion"`
	AssetHash     string       `json:"assetHash"`
	Tables        []any        `json:"tables"`
	Hashes        []any        `json:"hashes"`
	ServerCommand []any        `json:"serverCommand"`
	SystemLock    []SystemLock `json:"systemLock"`
	LotteryShop   []int        `json:"lotteryShop"`
}

// ClientIDResponse mirrors the captured /api/user/client/id response.
type ClientIDResponse struct {
	UserID        int64        `json:"userId"`
	ServerCommand []any        `json:"serverCommand"`
	SystemLock    []SystemLock `json:"systemLock"`
}

// FailedResponse is the client's FailedResponsePayload shape.
type FailedResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
