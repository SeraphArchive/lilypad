package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"lilypad/internal/codec"
	"lilypad/internal/config"
	"lilypad/internal/deltacomm"
	"lilypad/internal/model"
	"lilypad/internal/sign"
	"lilypad/internal/store"
	"lilypad/internal/store/postgres"
	"net/http/httptest"
	"testing"
	"time"
)

func safetyPost(t *testing.T, s *Server, path, id, token string, msg int64, body any) (int, map[string]any) {
	t.Helper()
	wire, err := codec.Encode(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", path, bytes.NewReader(wire))
	r.Header.Set("x-player-id", id)
	r.Header.Set("x-lilypad-session", token)
	r.Header.Set("x-msgid", fmt.Sprint(msg))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		return w.Code, nil
	}
	decoded, err := codec.Decode(w.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return w.Code, decoded
}
func safetyStory(clear bool, hashes any) map[string]any {
	return map[string]any{"savedTime": 100, "trigger": "StoryCleared", "hashes": hashes, "deltas": map[string]any{"putItems": map[string]any{"user_story": []any{map[string]any{"_masterStoryId": 1, "_isClear": clear}}}}}
}
func TestPushRetryAndStalePacketCannotRollbackNewProgress(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	token := testSessionToken(xuid + "a")
	initial := safetyStory(false, map[string]string{})
	_, first := safetyPost(t, s, "/api/user/push", xuid, token, 1001, initial)
	assertRPCSuccess(t, first)
	// A transport retry regenerates msgid but is still the same queued packet.
	_, retry := safetyPost(t, s, "/api/user/push", xuid, token, 1002, initial)
	assertRPCSuccess(t, retry)
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(retry)
	if !bytes.Equal(a, b) {
		t.Fatal("lost-ack retry did not return original response")
	}
	_, next := safetyPost(t, s, "/api/user/push", xuid, token, 1003, safetyStory(true, first["hashes"]))
	assertRPCSuccess(t, next)
	_, retry = safetyPost(t, s, "/api/user/push", xuid, token, 9999, initial)
	assertRPCSuccess(t, retry)
	_, conflict := safetyPost(t, s, "/api/user/push", xuid, token, 1004, safetyStory(false, first["hashes"]))
	if conflict == nil || asInt(conflict["code"]) != 2 {
		t.Fatal("old baseline was not rejected", conflict)
	}
	_, missing := safetyPost(t, s, "/api/user/push", xuid, token, 1005, safetyStory(false, nil))
	if missing == nil || asInt(missing["code"]) != 2 {
		t.Fatal("missing baseline bypassed protection", missing)
	}
	rows, err := pg.GetTables(context.Background(), uid, []string{"user_story"})
	if err != nil || rows["user_story"][0]["_isClear"] != true {
		t.Fatal("progress rolled back", rows, err)
	}
}
func TestClientWholeTableReplacementRejected(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	ctx := context.Background()
	hashes, err := pg.ApplyDeltas(ctx, uid, deltacomm.Deltas{PutItems: map[string][]store.Row{"user_story": {{"_masterStoryId": 1}, {"_masterStoryId": 2}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, out := safetyPost(t, s, "/api/user/push", xuid, testSessionToken(xuid+"a"), 1001, map[string]any{"hashes": hashes, "deltas": map[string]any{"replaceItems": map[string]any{"user_story": []any{}}}})
	if out == nil || asInt(out["code"]) == 0 {
		t.Fatal("client replacement accepted", out)
	}
	rows, _ := pg.GetTables(ctx, uid, []string{"user_story"})
	if len(rows["user_story"]) != 2 {
		t.Fatal(rows)
	}
}
func TestNoTokenCannotUseCounterAheadToWrite(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	ctx := context.Background()
	hashes, err := pg.ApplyDeltas(ctx, uid, deltacomm.Deltas{PutItems: map[string][]store.Row{"user_story": {{"_masterStoryId": 1, "_isClear": true}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	status, _ := safetyPost(t, s, "/api/user/push", xuid, "", 999999, safetyStory(false, hashes))
	if status != 401 {
		t.Fatal("tokenless writer admitted", status)
	}
	rows, _ := pg.GetTables(ctx, uid, []string{"user_story"})
	if rows["user_story"][0]["_isClear"] != true {
		t.Fatal(rows)
	}
}
func TestImportInvalidatesCachedPushAndPreservesOpaqueData(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	token := testSessionToken(xuid + "a")
	ctx := context.Background()
	old := safetyStory(false, map[string]string{})
	_, out := safetyPost(t, s, "/api/user/push", xuid, token, 1001, old)
	assertRPCSuccess(t, out)
	snapshot := map[string][]store.Row{"user_story": {{"_masterStoryId": 1, "_isClear": true, "_futureProgress": 99}}, "user_future_progress": {{"_futureId": 8, "_score": 100}}}
	if err := pg.ImportSnapshot(ctx, uid, snapshot, nil); err != nil {
		t.Fatal(err)
	}
	status, _ := safetyPost(t, s, "/api/user/push", xuid, token, 1002, old)
	if status != 401 {
		t.Fatal("old cached write survived import", status)
	}
	if err := pg.BeginSession(ctx, uid, "fresh-client", 2000); err != nil {
		t.Fatal(err)
	}
	// A new packet must be strictly later than the import's whole-second barrier.
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second)))
	hashes, _ := pg.AllHashes(ctx, uid)
	fresh := safetyStory(true, hashes)
	fresh["savedTime"] = time.Now().Unix()
	_, out = safetyPost(t, s, "/api/user/push", xuid, "fresh-client", 2001, fresh)
	assertRPCSuccess(t, out)
	saved, err := pg.ExportSnapshot(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if asInt(saved.Tables["user_story"][0]["_futureProgress"]) != 99 || len(saved.Tables["user_future_progress"]) != 1 {
		t.Fatal("old client erased external/newer data", saved)
	}
}
func TestSeededClientConfirmPullPushUsesOneVersion(t *testing.T) {
	ctx := context.Background()
	pg, err := postgres.New(ctx, integrityDSN(t), model.NewPlayerSeed())
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	s := New(&config.Config{}, sign.NoopSigner{}, pg, pg, nil, nil)
	xuid := fmt.Sprintf("aaaa%028x", testMessageID.Add(1))
	token := "fresh-process"
	_, login := safetyPost(t, s, "/api/user/client/id", xuid, token, 1000, map[string]any{"x_uid": xuid})
	id := fmt.Sprint(login["userId"])
	_, confirm := safetyPost(t, s, "/api/user/confirm", id, token, 1001, map[string]any{})
	assertRPCSuccess(t, confirm)
	_, pull := safetyPost(t, s, "/api/user/pull", id, token, 1002, map[string]any{"tables": []string{"user_story", "user_profile"}})
	assertRPCSuccess(t, pull)
	ch := confirm["hashes"].(map[string]any)
	ph := pull["hashes"].(map[string]any)
	for table, hash := range ph {
		if hash != ch[table] {
			t.Fatal("confirm/pull token mismatch", table)
		}
	}
	_, push := safetyPost(t, s, "/api/user/push", id, token, 1003, safetyStory(true, ph))
	assertRPCSuccess(t, push)
	_, pull = safetyPost(t, s, "/api/user/pull", id, token, 1004, map[string]any{"tables": []string{"user_story"}})
	if pull["hashes"].(map[string]any)["user_story"] != push["hashes"].(map[string]any)["user_story"] {
		t.Fatal("push/pull token mismatch")
	}
}

func TestRebasedPreImportQueueCannotOverwriteExternalProgress(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	ctx := context.Background()
	if err := pg.ImportSnapshot(ctx, uid, map[string][]store.Row{"user_story": {{"_masterStoryId": 1, "_isClear": true}}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := pg.BeginSession(ctx, uid, "restarted-process", 2000); err != nil {
		t.Fatal(err)
	}
	// DCC reads hashes from its current local token store when sending a queued
	// packet. Pulling after import can rebase an OLD packet onto NEW hashes.
	current, _ := pg.AllHashes(ctx, uid)
	_, out := safetyPost(t, s, "/api/user/push", xuid, "restarted-process", 2001, safetyStory(false, current))
	if out == nil || asInt(out["code"]) != 2 {
		t.Fatal("pre-import packet with refreshed baseline was accepted", out)
	}
	rows, _ := pg.GetTables(ctx, uid, []string{"user_story"})
	if rows["user_story"][0]["_isClear"] != true {
		t.Fatal("external progress overwritten", rows)
	}
	// Rejection preserves the packet's progress for manual recovery as well.
	// The live save remains authoritative; the refused packet is not applied.
	conn, err := pgx.Connect(ctx, integrityDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var archived []byte
	if err := conn.QueryRow(ctx, `SELECT new_data FROM player_data_history WHERE user_id=$1 AND table_name='@rejected_push' ORDER BY id DESC LIMIT 1`, uid).Scan(&archived); err != nil {
		t.Fatal("refused packet was not archived", err)
	}
	var packet map[string]any
	if err := json.Unmarshal(archived, &packet); err != nil {
		t.Fatal(err)
	}
	if asInt(packet["savedTime"]) != 100 {
		t.Fatal("wrong packet archived", packet)
	}
	puts := packet["deltas"].(map[string]any)["putItems"].(map[string]any)
	if puts["user_story"].([]any)[0].(map[string]any)["_isClear"] != false {
		t.Fatal("refused progress was not recoverable")
	}
}
