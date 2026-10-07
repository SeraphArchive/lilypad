package httpapi

import (
	"context"
	"fmt"
	"testing"

	"lilypad/internal/deltacomm"
	"lilypad/internal/store"
)

func TestWorldTransitionClearsLocalFlagsWithAtomicRetryAndBaseline(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	ctx := context.Background()
	initial := map[string][]store.Row{
		"user_world_state": {
			{"_index": 1, "_depth": 0, "_currentFieldLabel": "old-field", "_localFlag": []store.Row{{"_id": "old-local", "_value": 1}}},
			{"_index": 1, "_depth": 1, "_currentFieldLabel": "other-field", "_localFlag": []store.Row{{"_id": "other-local", "_value": 2}}},
		},
		"user_resume": {{"_index": 1, "_resumeLabel": "before-transition"}},
	}
	hashes, err := pg.ApplyDeltas(ctx, uid, deltacomm.Deltas{PutItems: initial}, nil)
	if err != nil {
		t.Fatal(err)
	}
	token := testSessionToken(xuid + "a")
	packet := map[string]any{
		"trigger": "WorldScriptInitialized", "savedTime": 100, "hashes": hashes,
		"deltas": deltacomm.Deltas{PutItems: map[string][]store.Row{
			"user_world_state": {{"_index": 1, "_depth": 0, "_currentFieldLabel": "new-field", "_localFlag": []store.Row{}}},
			"user_resume":      {{"_index": 1, "_resumeLabel": "after-transition"}},
		}},
	}
	_, first := safetyPost(t, s, "/api/user/push", xuid, token, 1001, packet)
	assertRPCSuccess(t, first)
	_, retry := safetyPost(t, s, "/api/user/push", xuid, token, 1002, packet)
	assertRPCSuccess(t, retry)
	if fmt.Sprint(first["hashes"]) != fmt.Sprint(retry["hashes"]) {
		t.Fatal("retry changed the committed response")
	}
	rows, err := pg.GetTables(ctx, uid, []string{"user_world_state", "user_resume"})
	if err != nil {
		t.Fatal(err)
	}
	world := rows["user_world_state"]
	if len(world) != 2 || world[0]["_currentFieldLabel"] != "new-field" || len(world[0]["_localFlag"].([]any)) != 0 || len(world[1]["_localFlag"].([]any)) != 1 || rows["user_resume"][0]["_resumeLabel"] != "after-transition" {
		t.Fatal("world transition failed to commit atomically or erased sibling state", rows)
	}
	packet["savedTime"] = 101 // A different queued packet with the old baseline.
	_, stale := safetyPost(t, s, "/api/user/push", xuid, token, 1003, packet)
	if stale == nil || asInt(stale["code"]) != 2 {
		t.Fatal("stale baseline bypassed after local flag fix", stale)
	}
}

func TestWorldTransitionWithUnknownFlagDataRejectsWholePacket(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	ctx := context.Background()
	hashes, err := pg.ApplyDeltas(ctx, uid, deltacomm.Deltas{PutItems: map[string][]store.Row{
		"user_world_state": {{"_index": 1, "_depth": 0, "_localFlag": []store.Row{{"_id": "future-local", "_value": 1, "_future": 99}}}},
		"user_resume":      {{"_index": 1, "_resumeLabel": "before-transition"}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, rejected := safetyPost(t, s, "/api/user/push", xuid, testSessionToken(xuid+"a"), 1001, map[string]any{
		"trigger": "WorldScriptInitialized", "savedTime": 100, "hashes": hashes,
		"deltas": deltacomm.Deltas{PutItems: map[string][]store.Row{
			"user_world_state": {{"_index": 1, "_depth": 0, "_localFlag": []store.Row{}}},
			"user_resume":      {{"_index": 1, "_resumeLabel": "after-transition"}},
		}},
	})
	if rejected == nil || asInt(rejected["code"]) != 1 {
		t.Fatal("unknown local data was discarded", rejected)
	}
	rows, err := pg.GetTables(ctx, uid, []string{"user_world_state", "user_resume"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows["user_world_state"][0]["_localFlag"].([]any)) != 1 || rows["user_resume"][0]["_resumeLabel"] != "before-transition" {
		t.Fatal("failed transition committed a partial save", rows)
	}
}
