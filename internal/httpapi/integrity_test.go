package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lilypad/internal/config"
	"lilypad/internal/gem"
	"lilypad/internal/master"
	"lilypad/internal/sign"
	"lilypad/internal/store"
	"lilypad/internal/store/postgres"
)

// Synthetic master definitions exercise complete HTTP/database transactions in CI.
func newIntegrityServer(t *testing.T) (*Server, *postgres.Store, string, int64) {
	return newIntegrityServerWithMaster(t, nil)
}

func newIntegrityServerWithMaster(t *testing.T, customize func(map[string][]map[string]any)) (*Server, *postgres.Store, string, int64) {
	t.Helper()
	dir := t.TempDir()
	defs := map[string][]map[string]any{
		"MasterMission":           {{"id": 500, "masterRewardGroupLabel": "reward"}},
		"MasterReward":            {{"id": 600, "groupLabel": "reward", "category": 99, "num": 20}, {"id": 601, "groupLabel": "batch", "category": 13, "num": 3}, {"id": 602, "groupLabel": "batch", "category": 1, "num": 1, "masterLabel": "ticket"}},
		"MasterItem":              {{"id": 700, "label": "ticket"}},
		"MasterLottery":           {{"id": 100, "masterLotteryRateGroupId": 200, "rewardCount": 1, "gemCost": 300, "consumeType": 1}},
		"MasterLotteryRate":       {{"id": 200, "groupId": 200, "rate": 100, "masterLotteryRewardGroupId": 300}},
		"MasterLotteryReward":     {{"id": 300, "groupId": 300, "rewardCategory": 9, "rewardId": 1001}},
		"MasterItemLottery":       {{"id": 400, "lotteryRewardGroupLabel": "pool", "consumeItemLabel": "ticket", "consumeItemCount": 1, "drawLimitPerRequest": 1, "dailyDrawLimit": 1, "totalDrawLimit": 1}},
		"MasterItemLotteryReward": {{"id": 401, "groupLabel": "pool", "ratio": 1, "rewardGroupLabel": "batch", "dropLimit": 1}},
	}
	if customize != nil {
		customize(defs)
	}
	for name, items := range defs {
		raw, _ := json.Marshal(map[string]any{"items": items})
		if err := os.WriteFile(filepath.Join(dir, name+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	md, err := master.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	pg, err := postgres.New(context.Background(), integrityDSN(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	xuid := fmt.Sprintf("synthetic-%s-%d", t.Name(), time.Now().UnixNano())
	acc, err := pg.GetOrCreate(context.Background(), xuid)
	if err != nil {
		t.Fatal(err)
	}
	if err := pg.BeginSession(context.Background(), acc.UserID, testSessionToken(xuid+"a"), 0); err != nil {
		t.Fatal(err)
	}
	return New(&config.Config{}, sign.NoopSigner{}, pg, pg, md, nil), pg, xuid, acc.UserID
}
func integrityDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("LILYPAD_TEST_DSN")
	if dsn == "" {
		t.Skip("LILYPAD_TEST_DSN not set")
	}
	return dsn
}
func assertRPCSuccess(t *testing.T, out map[string]any) {
	t.Helper()
	if out == nil || asInt(out["code"]) != 0 {
		t.Fatalf("RPC failed: %v", out)
	}
}

func TestMissionBatchAndLoopRetries(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	assertRPCSuccess(t, post(t, s, "/api/mission/receive", xuid, map[string]any{"masterMissionIds": []int{500, 500}}))
	b, _ := pg.Get(context.Background(), uid)
	if b.Free != 20 {
		t.Fatalf("duplicate mission batch credited %d", b.Free)
	}
	req := map[string]any{"receiveMissions": []any{map[string]any{"masterMissionId": 500, "amount": 1, "receivedCount": 1, "clearCount": 1}}}
	for i := 0; i < 2; i++ {
		assertRPCSuccess(t, post(t, s, "/api/mission/loop/receive", xuid, req))
	}
	b, _ = pg.Get(context.Background(), uid)
	if b.Free != 40 {
		t.Fatalf("loop retry credited again: %d", b.Free)
	}
}
func TestLotteryRetryRestoresOriginalResponse(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	if _, err := pg.Set(context.Background(), uid, gem.Balance{Free: 1000}); err != nil {
		t.Fatal(err)
	}
	fundedToken := testSessionToken(xuid + "funded")
	if err := pg.BeginSession(context.Background(), uid, fundedToken, 1000); err != nil {
		t.Fatal(err)
	}
	req := map[string]any{"masterLotteryId": 100, "rewardCount": 1, "gemCost": 300}
	_, first := postWithSession(t, s, "/api/lottery/draw", xuid, fundedToken, 12345, req)
	assertRPCSuccess(t, first)
	_, second := postWithSession(t, s, "/api/lottery/draw", xuid, fundedToken, 12345, req)
	assertRPCSuccess(t, second)
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Fatal("retry changed original response")
	}
	balance, _ := pg.Get(context.Background(), uid)
	if balance.Free != 700 {
		t.Fatal(balance)
	}
	req["rewardCount"] = 2
	_, conflict := postWithSession(t, s, "/api/lottery/draw", xuid, fundedToken, 12345, req)
	if asInt(conflict["code"]) == 0 {
		t.Fatal("reused ID accepted with different payload")
	}
}
func TestItemLotteryRejectsOverchargeAndAppliesProfileOnce(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	if err := pg.ImportTables(context.Background(), uid, map[string][]store.Row{"user_item": {{"_id": 700, "_num": 10}}, "user_profile": {{"_limitBreakPower": 0}}}); err != nil {
		t.Fatal(err)
	}
	out := post(t, s, "/api/item/lottery/draw", xuid, map[string]any{"masterItemLotteryId": 400, "drawCount": 5})
	if asInt(out["code"]) == 0 {
		t.Fatal("over-limit batch accepted")
	}
	snapshot, _ := pg.ExportSnapshot(context.Background(), uid)
	if asInt(snapshot.Tables["user_item"][0]["_num"]) != 10 {
		t.Fatal("rejected batch consumed items")
	}
	assertRPCSuccess(t, post(t, s, "/api/item/lottery/draw", xuid, map[string]any{"masterItemLotteryId": 400, "drawCount": 1}))
	snapshot, _ = pg.ExportSnapshot(context.Background(), uid)
	if asInt(snapshot.Tables["user_profile"][0]["_limitBreakPower"]) != 3 {
		t.Fatal(snapshot.Tables)
	}
	out = post(t, s, "/api/item/lottery/draw", xuid, map[string]any{"masterItemLotteryId": 400, "drawCount": 1})
	if asInt(out["code"]) == 0 {
		t.Fatal("daily/total limit ignored")
	}
}
func TestSessionTokenRejectsOverlappingCounters(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	if err := pg.BeginSession(context.Background(), uid, "device-a", 2000); err != nil {
		t.Fatal(err)
	}
	if err := pg.BeginSession(context.Background(), uid, "device-b", 1000); err != nil {
		t.Fatal(err)
	}
	// Replaying A's original login cannot revive the displaced process.
	if err := pg.BeginSession(context.Background(), uid, "device-a", 2000); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/user/confirm", nil)
	req.Header.Set("x-player-id", xuid)
	req.Header.Set("x-lilypad-session", "device-a")
	req.Header.Set("x-msgid", "9999")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatal(rec.Code)
	}
	req.Header.Set("x-lilypad-session", "device-b")
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if err := pg.ImportSnapshot(context.Background(), uid, map[string][]store.Row{"user_item": {{"_id": 1, "_num": 100}}}, nil); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("import failed to revoke counter-ahead session: %d", rec.Code)
	}
	if err := pg.BeginSession(context.Background(), uid, "device-b", 1000); err != store.ErrSession {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatal("replayed pre-import login reopened session")
	}
	if err := pg.BeginSession(context.Background(), uid, "device-b", 1001); err != store.ErrSession {
		t.Fatal("pre-import process was allowed to renew its retired session", err)
	}
	if err := pg.BeginSession(context.Background(), uid, "device-c", 1001); err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-lilypad-session", "device-c")
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal("fresh login did not reopen session")
	}
}

type interleavedRequestStore struct {
	*postgres.Store
	uid int64
}

func (s *interleavedRequestStore) RunRequest(ctx context.Context, uid, generation int64, key, digest string, fn func(context.Context) (store.Response, error)) (store.Response, error) {
	if err := s.ImportSnapshot(ctx, uid, map[string][]store.Row{"user_item": {{"_id": 1, "_num": 100}}}, nil); err != nil {
		return store.Response{}, err
	}
	return s.Store.RunRequest(ctx, uid, generation, key, digest, fn)
}
func TestAdmittedPushCannotOverwriteImportedSave(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	s.store = &interleavedRequestStore{pg, uid}
	status, _ := postWithMsgid(t, s, "/api/user/push", xuid, 1001, map[string]any{"deltas": map[string]any{"putItems": map[string]any{"user_item": []any{map[string]any{"_id": 1, "_num": 1}}}}})
	if status != 401 {
		t.Fatal(status)
	}
	snapshot, _ := pg.ExportSnapshot(context.Background(), uid)
	if asInt(snapshot.Tables["user_item"][0]["_num"]) != 100 {
		t.Fatal(snapshot.Tables)
	}
}
func TestHashMatchesJSONBAfterExponentNormalization(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	assertRPCSuccess(t, post(t, s, "/api/user/push", xuid, map[string]any{"deltas": map[string]any{"putItems": map[string]any{"user_life": []any{map[string]any{"_num": json.Number("1e3")}}}}}))
	snapshot, _ := pg.ExportSnapshot(context.Background(), uid)
	hashes, err := pg.AllHashes(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Hashes["user_life"] != hashes["user_life"] || asInt(snapshot.Tables["user_life"][0]["_num"]) != 1000 {
		t.Fatal("saved version/data differs after JSONB read")
	}
}
func TestMissingMasterPreservesRewardsAndItems(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	s.master = nil
	pg.ImportTables(context.Background(), uid, map[string][]store.Row{"user_client_gift": {{"_giftId": 1, "_masterRewardId": 600, "_isReceived": false}}, "user_item": {{"_id": 700, "_num": 2}}})
	out := post(t, s, "/api/gift/receive", xuid, map[string]any{"clientGiftIds": []int{1}})
	if asInt(out["code"]) == 0 {
		t.Fatal("missing-master gift accepted")
	}
	out = post(t, s, "/api/life/recover", xuid, map[string]any{"recoveryNum": 1, "masterRecoverItemLabel": "ticket", "consumeItemCount": 1})
	if asInt(out["code"]) == 0 {
		t.Fatal("missing-master item recovery accepted")
	}
	snapshot, _ := pg.ExportSnapshot(context.Background(), uid)
	if asBool(snapshot.Tables["user_client_gift"][0]["_isReceived"]) || asInt(snapshot.Tables["user_item"][0]["_num"]) != 2 {
		t.Fatal(snapshot.Tables)
	}
}

func TestUnknownGiftDefinitionDoesNotConsumeGift(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	if err := pg.ImportTables(context.Background(), uid, map[string][]store.Row{"user_client_gift": {{"_giftId": 1, "_masterRewardId": 9999, "_isReceived": false}}}); err != nil {
		t.Fatal(err)
	}
	out := post(t, s, "/api/gift/receive", xuid, map[string]any{"clientGiftIds": []int{1}})
	if asInt(out["code"]) == 0 {
		t.Fatal("unknown reward settled")
	}
	snapshot, err := pg.ExportSnapshot(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if asBool(snapshot.Tables["user_client_gift"][0]["_isReceived"]) {
		t.Fatal("gift lost")
	}
}
func TestDailyCreatesStampAndUsesResetWeekday(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	pg.ImportTables(context.Background(), uid, map[string][]store.Row{"user_profile": {{"_userName": "partial"}}})
	for i := 0; i < 2; i++ {
		assertRPCSuccess(t, post(t, s, "/api/daily/update", xuid, nil))
	}
	snapshot, _ := pg.ExportSnapshot(context.Background(), uid)
	for _, r := range snapshot.Tables["user_record"] {
		if asInt(r["_type"]) == 7 && asInt(r["_value"]) != 1 {
			t.Fatal(snapshot.Tables)
		}
	}
	now := time.Date(2026, 10, 1, 3, 0, 0, 0, time.FixedZone("JST", 9*3600)).Unix()
	if jstWeekday(now) != time.Wednesday {
		t.Fatal("wrong reset weekday")
	}
}

func TestBoxCompletionStopsDrawsAndChargesActualCount(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServer(t)
	f, _ := s.master.File("MasterItemLottery")
	row := f.ByID[400]
	row["drawLimitPerRequest"] = 10
	row["dailyDrawLimit"] = 0
	row["totalDrawLimit"] = 0
	row["afterPickupCompleteType"] = 1
	row["completeRewardGroupLabel"] = "reward"
	rewards, _ := s.master.File("MasterItemLotteryReward")
	rewards.Items[0]["isPickup"] = true
	if err := pg.ImportTables(context.Background(), uid, map[string][]store.Row{"user_item": {{"_id": 700, "_num": 10}}, "user_profile": {{"_limitBreakPower": 0}}}); err != nil {
		t.Fatal(err)
	}
	assertRPCSuccess(t, post(t, s, "/api/item/lottery/draw", xuid, map[string]any{"masterItemLotteryId": 400, "drawCount": 5}))
	snapshot, err := pg.ExportSnapshot(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	// One consumed ticket, one awarded ticket from the regular reward group.
	if asInt(snapshot.Tables["user_item"][0]["_num"]) != 10 || snapshot.Balance.Free != 20 || asInt(snapshot.Tables["user_item_lottery"][0]["_totalDrawCount"]) != 1 || !asBool(snapshot.Tables["user_item_lottery"][0]["_isTerminated"]) {
		t.Fatal(snapshot)
	}
	if len(snapshot.Tables["user_item_lottery_result"]) != 1 {
		t.Fatal("result count differs from actual draws")
	}
}
