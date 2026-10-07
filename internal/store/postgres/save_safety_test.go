package postgres

import (
	"context"
	"errors"
	"fmt"
	"lilypad/internal/deltacomm"
	"lilypad/internal/gem"
	"lilypad/internal/store"
	"testing"
	"time"
)

func safetyAccount(t *testing.T) (*Store, int64) {
	t.Helper()
	s := freshStore(t, nil)
	a, err := s.GetOrCreate(context.Background(), "save-safety")
	if err != nil {
		t.Fatal(err)
	}
	return s, a.UserID
}
func storyDelta(clear bool) deltacomm.Deltas {
	return deltacomm.Deltas{PutItems: map[string][]Row{"user_story": {{"_masterStoryId": 1, "_isClear": clear}}}}
}
func TestClientPushRequiresEveryStoredBaseline(t *testing.T) {
	s, uid := safetyAccount(t)
	ctx := context.Background()
	if _, err := s.ApplyDeltas(ctx, uid, storyDelta(true), nil); err != nil {
		t.Fatal(err)
	}
	for _, base := range []map[string]string{{}, {"user_story": ""}, {"unrelated": "anything"}} {
		if _, err := s.ApplyDeltas(ctx, uid, storyDelta(false), base); !errors.Is(err, store.ErrConcurrency) {
			t.Fatalf("missing baseline allowed overwrite: %v", err)
		}
	}
	rows, _ := s.GetTables(ctx, uid, []string{"user_story"})
	if rows["user_story"][0]["_isClear"] != true {
		t.Fatal("progress changed")
	}
}

func TestClientSaveBoundaryRejectsWholeTableReplacement(t *testing.T) {
	s, uid := safetyAccount(t)
	ctx := context.Background()
	base, err := s.ApplyDeltas(ctx, uid, storyDelta(true), nil)
	if err != nil {
		t.Fatal(err)
	}
	delta := deltacomm.Deltas{ReplaceItems: map[string][]Row{"user_story": {}}}
	if _, err := s.ApplyClientDeltas(ctx, uid, delta, base, time.Now().Unix()); err == nil {
		t.Fatal("client storage boundary accepted a full replacement")
	}
	after, err := s.ExportSnapshot(ctx, uid)
	if err != nil || len(after.Tables["user_story"]) != 1 || after.Hashes["user_story"] != base["user_story"] {
		t.Fatal("rejected replacement changed save", after, err)
	}
}

func TestNegativeGemMutationRollsBackSaveAndWallet(t *testing.T) {
	s, uid := safetyAccount(t)
	ctx := context.Background()
	if _, err := s.Add(ctx, uid, 100); err != nil {
		t.Fatal(err)
	}
	base, err := s.ApplyDeltas(ctx, uid, storyDelta(true), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []store.GemChange{{Add: -1}, {Add: 2, Consume: -1}, {Add: -1, Consume: 10}} {
		if _, err := s.MutateWithGems(ctx, uid, nil, func(map[string][]Row) (deltacomm.Deltas, store.GemChange, error) {
			return storyDelta(false), change, nil
		}); err == nil {
			t.Fatal("negative gem mutation accepted", change)
		}
		after, err := s.ExportSnapshot(ctx, uid)
		if err != nil || after.Balance.Free != 100 || after.Hashes["user_story"] != base["user_story"] {
			t.Fatal("rejected mutation changed save or wallet", after, err)
		}
	}
}
func TestExternalSQLChangesFenceOldWritersAndNeverReuseTokens(t *testing.T) {
	s, uid := safetyAccount(t)
	ctx := context.Background()
	first, err := s.ApplyDeltas(ctx, uid, storyDelta(true), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BeginSession(ctx, uid, "device", 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE user_state SET rows='[{"_masterStoryId":1,"_isClear":false}]' WHERE user_id=$1 AND table_name='user_story'`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckSession(ctx, uid, "device", 999999); !errors.Is(err, store.ErrSession) {
		t.Fatalf("external edit did not fence live writer: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE user_state SET rows='[{"_masterStoryId":1,"_isClear":true}]' WHERE user_id=$1 AND table_name='user_story'`, uid); err != nil {
		t.Fatal(err)
	}
	after, err := s.AllHashes(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if first["user_story"] == after["user_story"] {
		t.Fatal("ABA edit reused old token")
	}
	if _, err := s.ApplyDeltas(ctx, uid, storyDelta(false), first); !errors.Is(err, store.ErrConcurrency) {
		t.Fatalf("pre-edit baseline accepted: %v", err)
	}
}
func TestExternalDeleteLeavesPullableTombstone(t *testing.T) {
	s, uid := safetyAccount(t)
	ctx := context.Background()
	old, err := s.ApplyDeltas(ctx, uid, storyDelta(true), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM user_state WHERE user_id=$1 AND table_name='user_story'`, uid); err != nil {
		t.Fatal(err)
	}
	hashes, err := s.AllHashes(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if hashes["user_story"] == "" || hashes["user_story"] == old["user_story"] {
		t.Fatal("deleted table disappeared from confirm or kept old token")
	}
	rows, err := s.GetTables(ctx, uid, []string{"user_story"})
	if err != nil || len(rows["user_story"]) != 0 {
		t.Fatal(rows, err)
	}
	if _, err := s.ApplyDeltas(ctx, uid, storyDelta(true), old); !errors.Is(err, store.ErrConcurrency) {
		t.Fatal("old writer resurrected deleted table", err)
	}
}
func TestRawSQLAndImportsRetainRecoverableBeforeImages(t *testing.T) {
	s, uid := safetyAccount(t)
	ctx := context.Background()
	if _, err := s.ApplyDeltas(ctx, uid, storyDelta(true), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM user_state WHERE user_id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM player_data_history WHERE user_id=$1 AND table_name='user_story' AND operation='DELETE' AND old_data->0->>'_isClear'='true'`, uid).Scan(&n); err != nil || n != 1 {
		t.Fatalf("missing recoverable before image: n=%d err=%v", n, err)
	}
	if _, err := s.Set(ctx, uid, gem.Balance{Free: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE gems SET free=20 WHERE user_id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM player_data_history WHERE user_id=$1 AND table_name='@gems' AND old_data->>'free'='100'`, uid).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}
func TestRestartAndNewSeedPreserveExistingSaveAndBaseline(t *testing.T) {
	s, uid := safetyAccount(t)
	ctx := context.Background()
	old, err := s.ApplyDeltas(ctx, uid, storyDelta(true), nil)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := New(ctx, dsnOrSkip(t), map[string][]Row{"user_story": {{"_masterStoryId": 1, "_isClear": false}}})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.GetOrCreate(ctx, "save-safety"); err != nil {
		t.Fatal(err)
	}
	rows, _ := reopened.GetTables(ctx, uid, []string{"user_story"})
	hashes, _ := reopened.AllHashes(ctx, uid)
	if rows["user_story"][0]["_isClear"] != true || hashes["user_story"] != old["user_story"] {
		t.Fatal("restart/new seed changed existing save")
	}
}
func TestTokenlessLoginCannotAuthorizeWrites(t *testing.T) {
	s, uid := safetyAccount(t)
	ctx := context.Background()
	if err := s.BeginSession(ctx, uid, "", 1000); !errors.Is(err, store.ErrSession) {
		t.Fatalf("tokenless login accepted: %v", err)
	}
	if _, err := s.CheckSession(ctx, uid, "", 999999); !errors.Is(err, store.ErrSession) {
		t.Fatalf("tokenless request admitted: %v", err)
	}
}

func TestConflictDoNothingCannotChangeSaveVersionOrHistory(t *testing.T) {
	s, uid := safetyAccount(t)
	ctx := context.Background()
	old, err := s.ApplyDeltas(ctx, uid, storyDelta(true), nil)
	if err != nil {
		t.Fatal(err)
	}
	var before int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM player_data_history WHERE user_id=$1`, uid).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedNewPlayer(ctx, uid, map[string][]Row{"user_story": {{"_masterStoryId": 1, "_isClear": false}}}); err != nil {
		t.Fatal(err)
	}
	// A direct SQL conflict-ignore is also not a successful data mutation.
	if _, err := s.pool.Exec(ctx, `INSERT INTO user_state(user_id,table_name,rows,hash) VALUES($1,'user_story','[]','ignored') ON CONFLICT DO NOTHING`, uid); err != nil {
		t.Fatal(err)
	}
	after, _ := s.AllHashes(ctx, uid)
	snapshot, err := s.ExportSnapshot(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM player_data_history WHERE user_id=$1`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != before || after["user_story"] != old["user_story"] || snapshot.Hashes["user_story"] != old["user_story"] || snapshot.Tables["user_story"][0]["_isClear"] != true {
		t.Fatal("ignored insert changed save/token/history", n, before, after, snapshot)
	}
}

func TestNestedMutationDoesNotAlterComparisonBase(t *testing.T) {
	s, uid := safetyAccount(t)
	ctx := context.Background()
	if _, err := s.ApplyDeltas(ctx, uid, deltacomm.Deltas{PutItems: map[string][]Row{"user_profile": {{"nested": map[string]any{"progress": 1, "future": 99}}}}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MutateUnderLock(ctx, uid, []string{"user_profile"}, func(current map[string][]Row) (deltacomm.Deltas, error) {
		current["user_profile"][0]["nested"].(map[string]any)["progress"] = 2
		return deltacomm.Deltas{PutItems: current}, nil
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.GetTables(ctx, uid, []string{"user_profile"})
	if err != nil {
		t.Fatal(err)
	}
	nested := rows["user_profile"][0]["nested"].(map[string]any)
	if fmt.Sprint(nested["progress"]) != "2" || fmt.Sprint(nested["future"]) != "99" {
		t.Fatal("nested mutation was lost", rows)
	}
}

func TestStandaloneWalletReplacementRevokesOldSession(t *testing.T) {
	s, uid := safetyAccount(t)
	ctx := context.Background()
	if err := s.BeginSession(ctx, uid, "old-client", 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set(ctx, uid, gem.Balance{Free: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckSession(ctx, uid, "old-client", 999999); !errors.Is(err, store.ErrSession) {
		t.Fatal("external wallet replacement left old session active", err)
	}
}

func TestExternalEditAfterAdmissionFencesWholeRequest(t *testing.T) {
	s, uid := safetyAccount(t)
	ctx := context.Background()
	if _, err := s.ApplyDeltas(ctx, uid, storyDelta(false), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginSession(ctx, uid, "device", 1000); err != nil {
		t.Fatal(err)
	}
	generation, err := s.CheckSession(ctx, uid, "device", 1001)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE user_state SET rows='[{"_masterStoryId":1,"_isClear":true}]' WHERE user_id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = s.RunRequest(ctx, uid, generation, "old-operation", "digest", func(context.Context) (store.Response, error) { called = true; return store.Response{Status: 200}, nil })
	if !errors.Is(err, store.ErrSession) || called {
		t.Fatal("admitted stale request executed after external edit", err)
	}
	if err := s.BeginSession(ctx, uid, "device", 1000000); !errors.Is(err, store.ErrSession) {
		t.Fatal("old process renewed retired session", err)
	}
}

func TestDelayedRetryAfterManyOperationsAndReloginDoesNotChargeAgain(t *testing.T) {
	s, uid := safetyAccount(t)
	ctx := context.Background()
	if err := s.BeginSession(ctx, uid, "device-a", 1000); err != nil {
		t.Fatal(err)
	}
	generation, err := s.CheckSession(ctx, uid, "device-a", 1001)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	paid := func(context.Context) (store.Response, error) {
		calls++
		return store.Response{Status: 200, Body: []byte("original paid result")}, nil
	}
	first, err := s.RunRequest(ctx, uid, generation, "paid", "digest", paid)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 270; i++ {
		if _, err := s.RunRequest(ctx, uid, generation, fmt.Sprint(i), "other", func(context.Context) (store.Response, error) { return store.Response{Status: 200}, nil }); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.BeginSession(ctx, uid, "device-b", 2000); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginSession(ctx, uid, "device-a", 1002); err != nil {
		t.Fatal(err)
	}
	generation, err = s.CheckSession(ctx, uid, "device-a", 1003)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.RunRequest(ctx, uid, generation, "paid", "digest", paid)
	if err != nil || calls != 1 || string(retry.Body) != string(first.Body) {
		t.Fatal("delayed/relogged retry executed twice", calls, err)
	}
}

func TestAccountIdentityEditsCannotOrphanExistingSave(t *testing.T) {
	s, uid := safetyAccount(t)
	ctx := context.Background()
	if _, err := s.ApplyDeltas(ctx, uid, storyDelta(true), nil); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{`UPDATE accounts SET user_id=user_id+100 WHERE user_id=$1`, `UPDATE accounts SET x_uid='replacement' WHERE user_id=$1`, `DELETE FROM accounts WHERE user_id=$1`} {
		if _, err := s.pool.Exec(ctx, sql, uid); err == nil {
			t.Fatal("identity edit orphaned save", sql)
		}
	}
	a, err := s.GetOrCreate(ctx, "save-safety")
	if err != nil || a.UserID != uid {
		t.Fatal("account appeared new", a, err)
	}
	rows, _ := s.GetTables(ctx, uid, []string{"user_story"})
	if rows["user_story"][0]["_isClear"] != true {
		t.Fatal(rows)
	}
}

func TestExternalMetadataEditsCannotHideSaveOrDestroyRecovery(t *testing.T) {
	s, uid := safetyAccount(t)
	ctx := context.Background()
	old, err := s.ApplyDeltas(ctx, uid, storyDelta(true), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{`DELETE FROM user_state_tokens WHERE user_id=$1`, `UPDATE user_state_tokens SET hash='' WHERE user_id=$1`, `DELETE FROM player_data_history WHERE user_id=$1`} {
		if _, err := s.pool.Exec(ctx, sql, uid); err == nil {
			t.Fatal("metadata could silently hide saved state", sql)
		}
	}
	hashes, err := s.AllHashes(ctx, uid)
	if err != nil || hashes["user_story"] != old["user_story"] {
		t.Fatal(hashes, err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM player_data_history WHERE user_id=$1`, uid).Scan(&n); err != nil || n == 0 {
		t.Fatal("recovery lost", n, err)
	}
}
