package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"lilypad/internal/config"
	"lilypad/internal/deltacomm"
	"lilypad/internal/master"
	"lilypad/internal/model"
	"lilypad/internal/sign"
	"lilypad/internal/store/postgres"
)

// newEconomyServer builds a server with a real store and master data; skips
// unless both LILYPAD_TEST_DSN and LILYPAD_DATA_DIR are set.
func newEconomyServer(t *testing.T) *Server {
	t.Helper()
	dsn := os.Getenv("LILYPAD_TEST_DSN")
	dir := os.Getenv("LILYPAD_DATA_DIR")
	if dsn == "" || dir == "" {
		t.Skip("LILYPAD_TEST_DSN and LILYPAD_DATA_DIR required for economy integration test")
	}
	pg, err := postgres.New(context.Background(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	md, err := master.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return New(&config.Config{}, sign.NoopSigner{}, pg, pg, md, nil)
}

// newSeededEconomyServer is like newEconomyServer but seeds new accounts with
// the new-player template (so gift/mission tests have starter state).
func newSeededEconomyServer(t *testing.T) *Server {
	t.Helper()
	dsn := os.Getenv("LILYPAD_TEST_DSN")
	dir := os.Getenv("LILYPAD_DATA_DIR")
	if dsn == "" || dir == "" {
		t.Skip("LILYPAD_TEST_DSN and LILYPAD_DATA_DIR required for economy integration test")
	}
	pg, err := postgres.New(context.Background(), dsn, model.NewPlayerSeed())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	md, err := master.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return New(&config.Config{}, sign.NoopSigner{}, pg, pg, md, nil)
}

func TestRandomSetupPersists(t *testing.T) {
	s := newEconomyServer(t)
	xuid := fmt.Sprintf("rng-%d", time.Now().UnixNano())
	out := post(t, s, "/api/random/setup", xuid, map[string]any{"hdr": "", "dummy": 0})
	tbls := out["tables"].(map[string]any)["putItems"].(map[string]any)
	if len(tbls["user_random"].([]any)) != 13 {
		t.Fatalf("expected 13 user_random rows: %v", tbls["user_random"])
	}
	// pull it back
	pull := post(t, s, "/api/user/pull", xuid, map[string]any{"hdr": "", "tables": []any{"user_random"}})
	if len(pull["tables"].(map[string]any)["user_random"].([]any)) != 13 {
		t.Fatal("user_random did not persist")
	}
}

// findRollableLottery returns a lottery id whose rate group resolves to rewards.
func findRollableLottery(s *Server) (int64, bool) {
	lot, ok := s.master.File("MasterLottery")
	if !ok {
		return 0, false
	}
	for id := range lot.ByID {
		if _, err := s.master.Roll(id, 1, func(n int) int { return 0 }); err == nil {
			return id, true
		}
	}
	return 0, false
}

func findRollableZeroGemLottery(s *Server) (int64, bool) {
	lot, ok := s.master.File("MasterLottery")
	if !ok {
		return 0, false
	}
	for id := range lot.ByID {
		ct, cost, ok := s.master.LotteryConsume(id, 1)
		if !ok || ct == master.LotteryConsumeChargeGem || cost != 0 {
			continue
		}
		if _, err := s.master.Roll(id, 1, func(n int) int { return 0 }); err == nil {
			return id, true
		}
	}
	return 0, false
}

func TestLotteryDrawGrantsAndPersistsCard(t *testing.T) {
	s := newEconomyServer(t)
	lotteryID, ok := findRollableZeroGemLottery(s)
	if !ok {
		t.Skip("no rollable zero-gem lottery in master data")
	}
	xuid := fmt.Sprintf("gacha-%d", time.Now().UnixNano())

	out := post(t, s, "/api/lottery/draw", xuid, map[string]any{
		"hdr": "", "masterLotteryId": lotteryID, "rewardCount": 1,
	})
	if out["code"].(json.Number).String() != "0" {
		t.Fatalf("draw failed: %v", out)
	}
	put := out["tables"].(map[string]any)["putItems"].(map[string]any)
	cards, _ := put["user_card"].([]any)

	// pull user_card and confirm a card persisted (if the roll yielded a card).
	pull := post(t, s, "/api/user/pull", xuid, map[string]any{"hdr": "", "tables": []any{"user_card"}})
	persisted := pull["tables"].(map[string]any)["user_card"].([]any)
	if len(cards) > 0 && len(persisted) == 0 {
		t.Fatal("granted card did not persist across pull")
	}
}

// A live paid banner is typically absent from (or zero in) the local master
// snapshot, and the client can send
// consumeType:0 with gemCost>0 on the paid pull. The draw must still consume the
// declared gems. We pick a lottery the master treats as free so the master path
// does not override the client's declared cost.
func TestLotteryDrawConsumesClientGemCostWithConsumeTypeZero(t *testing.T) {
	s := newEconomyServer(t)
	lotteryID, ok := findRollableZeroGemLottery(s)
	if !ok {
		t.Skip("no rollable zero-gem lottery in master data")
	}
	xuid := fmt.Sprintf("gacha-charge-%d", time.Now().UnixNano())
	userID, ok := s.resolvePlayer(context.Background(), xuid)
	if !ok {
		t.Fatal("resolve player failed")
	}
	if _, err := s.gems.Add(context.Background(), userID, 500); err != nil {
		t.Fatal(err)
	}

	out := post(t, s, "/api/lottery/draw", xuid, map[string]any{
		"hdr": "", "masterLotteryId": lotteryID, "rewardCount": 1,
		"consumeType": 0, "gemCost": 300,
	})
	if out["code"].(json.Number).String() != "0" {
		t.Fatalf("draw failed: %v", out)
	}
	b, err := s.gems.Get(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Free != 200 || b.Paid != 0 {
		t.Fatalf("gem balance after draw = %+v, want free=200 paid=0", b)
	}
}

// On a TicketOrGem banner the client echoes the master's ticketCost even when
// paying gems (consumeType:0 = Gem choice). The ticket item must only be
// consumed on an explicit Ticket choice (consumeType:2) — a gem-paid pull
// charges gems alone. Lottery 62007892: consumeType 3, gemCost 3000,
// consumeItemId 7040217 (ticket).
func TestLotteryDrawGemPaidDoesNotConsumeTicket(t *testing.T) {
	s := newEconomyServer(t)
	if _, ok := s.master.Lottery(62007892); !ok {
		t.Skip("lottery 62007892 not in master data")
	}
	xuid := fmt.Sprintf("gacha-ticket-%d", time.Now().UnixNano())
	userID, ok := s.resolvePlayer(context.Background(), xuid)
	if !ok {
		t.Fatal("resolve player failed")
	}
	if _, err := s.gems.Add(context.Background(), userID, 3000); err != nil {
		t.Fatal(err)
	}
	// Own 5 of the ticket item.
	post(t, s, "/api/user/push", xuid, map[string]any{
		"hdr": "", "trigger": "Synced",
		"deltas": map[string]any{"putItems": map[string]any{
			"user_item": []any{map[string]any{"_id": 7040217, "_num": 5, "_reservedNum": 0, "_isAlreadyPossessed": true, "_totalNum": 5, "_isLocked": false}},
		}},
	})

	out := post(t, s, "/api/lottery/draw", xuid, map[string]any{
		"hdr": "", "masterLotteryId": 62007892, "rewardCount": 10,
		"consumeType": 0, "gemCost": 3000, "ticketCost": 1,
	})
	if out["code"].(json.Number).String() != "0" {
		t.Fatalf("draw failed: %v", out)
	}
	b, err := s.gems.Get(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Free != 0 {
		t.Fatalf("gem balance after gem draw = %+v, want 0", b)
	}
	pull := post(t, s, "/api/user/pull", xuid, map[string]any{"hdr": "", "tables": []any{"user_item"}})
	for _, r := range pull["tables"].(map[string]any)["user_item"].([]any) {
		row := r.(map[string]any)
		if row["_id"].(json.Number).String() == "7040217" && row["_num"].(json.Number).String() != "5" {
			t.Fatalf("ticket item consumed on a gem-paid draw: %v", row)
		}
	}

	// The explicit ticket choice consumes the ticket and charges no gems.
	if _, err := s.gems.Add(context.Background(), userID, 3000); err != nil {
		t.Fatal(err)
	}
	out = post(t, s, "/api/lottery/draw", xuid, map[string]any{
		"hdr": "", "masterLotteryId": 62007892, "rewardCount": 10,
		"consumeType": 2, "gemCost": 0, "ticketCost": 1,
	})
	if out["code"].(json.Number).String() != "0" {
		t.Fatalf("ticket draw failed: %v", out)
	}
	b, err = s.gems.Get(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Free != 3000 {
		t.Fatalf("ticket draw must not charge gems: %+v", b)
	}
	pull = post(t, s, "/api/user/pull", xuid, map[string]any{"hdr": "", "tables": []any{"user_item"}})
	found := false
	for _, r := range pull["tables"].(map[string]any)["user_item"].([]any) {
		row := r.(map[string]any)
		if row["_id"].(json.Number).String() == "7040217" {
			found = true
			if row["_num"].(json.Number).String() != "4" {
				t.Fatalf("ticket count after ticket draw = %v, want 4", row["_num"])
			}
		}
	}
	if !found {
		t.Fatal("ticket item row missing after ticket draw")
	}
}

func TestMissionReceiveMarksReceived(t *testing.T) {
	s := newEconomyServer(t)
	xuid := fmt.Sprintf("mission-%d", time.Now().UnixNano())
	out := post(t, s, "/api/mission/receive", xuid, map[string]any{
		"hdr": "", "masterMissionIds": []any{79030602, 79030603}, "enableSubscriptionBonus": false,
	})
	put := out["tables"].(map[string]any)["putItems"].(map[string]any)
	rows := put["user_mission"].([]any)
	if len(rows) != 2 {
		t.Fatalf("expected 2 mission rows, got %d", len(rows))
	}
	if rows[0].(map[string]any)["_state"].(json.Number).String() != "3" {
		t.Fatalf("mission not marked received (state 3): %v", rows[0])
	}
	// persisted
	pull := post(t, s, "/api/user/pull", xuid, map[string]any{"hdr": "", "tables": []any{"user_mission"}})
	if len(pull["tables"].(map[string]any)["user_mission"].([]any)) != 2 {
		t.Fatal("missions did not persist")
	}
}

func TestGiftReceiveGrantsItemAndMarksReceived(t *testing.T) {
	s, pg, xuid, uid := newIntegrityServerWithMaster(t, func(defs map[string][]map[string]any) {
		defs["MasterItem"] = append(defs["MasterItem"], map[string]any{"id": 701, "label": "second-item"})
	})
	// The public starter save has no campaign gifts. Arrange this account's
	// synthetic inbox explicitly, independently of optional master data.
	if _, err := pg.ApplyDeltas(context.Background(), uid, deltacomm.Deltas{PutItems: map[string][]deltacomm.Row{
		"user_server_gift": {
			{"_giftId": 1, "_rewardCategory": 1, "_itemId": 700, "_itemNum": 100, "_isReceived": false},
			{"_giftId": 2, "_rewardCategory": 1, "_itemId": 701, "_itemNum": 10, "_isReceived": false},
		},
	}}, nil); err != nil {
		t.Fatal(err)
	}
	out := post(t, s, "/api/gift/receive", xuid, map[string]any{
		"hdr": "", "clientGiftIds": []any{}, "serverGiftIds": []any{1, 2},
	})
	assertRPCSuccess(t, out)
	put := out["tables"].(map[string]any)["putItems"].(map[string]any)
	items, ok := put["user_item"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("expected 2 granted items (gifts 1,2 are distinct items), got %v", put["user_item"])
	}
	// gift 1 grants item 700 x100
	pull := post(t, s, "/api/user/pull", xuid, map[string]any{"hdr": "", "tables": []any{"user_item", "user_server_gift"}})
	var found bool
	for _, it := range pull["tables"].(map[string]any)["user_item"].([]any) {
		m := it.(map[string]any)
		if m["_id"].(json.Number).String() == "700" {
			found = true
			if m["_num"].(json.Number).String() != "100" {
				t.Fatalf("expected item 700 total 100, got %v", m["_num"])
			}
		}
	}
	if !found {
		t.Fatal("granted item not persisted")
	}
	// gifts 1,2 must be marked received
	recv := 0
	for _, g := range pull["tables"].(map[string]any)["user_server_gift"].([]any) {
		m := g.(map[string]any)
		id := m["_giftId"].(json.Number).String()
		if (id == "1" || id == "2") && m["_isReceived"] == true {
			recv++
		}
	}
	if recv != 2 {
		t.Fatalf("expected 2 gifts marked received, got %d", recv)
	}
}

func TestGiftReceiveGrantsSeededQuartz(t *testing.T) {
	s, pg, xuid, userID := newIntegrityServer(t)
	if _, err := pg.ApplyDeltas(context.Background(), userID, deltacomm.Deltas{PutItems: map[string][]deltacomm.Row{
		"user_server_gift": {{"_giftId": 13, "_rewardCategory": 99, "_itemNum": 3000, "_isReceived": false}},
	}}, nil); err != nil {
		t.Fatal(err)
	}
	out := post(t, s, "/api/gift/receive", xuid, map[string]any{
		"hdr": "", "clientGiftIds": []any{}, "serverGiftIds": []any{13},
	})
	if out["code"].(json.Number).String() != "0" {
		t.Fatalf("gift receive failed: %v", out)
	}
	b, err := s.gems.Get(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Free != 3000 || b.Paid != 0 {
		t.Fatalf("gem balance = %+v, want free=3000 paid=0", b)
	}
	assertRPCSuccess(t, post(t, s, "/api/gift/receive", xuid, map[string]any{"serverGiftIds": []int{13}}))
	if after, err := pg.Get(context.Background(), userID); err != nil || after != b {
		t.Fatal("repeated gift claim changed wallet", after, err)
	}
}

// Client gifts with a hard-currency reward are credited by the server at
// receive time: the client cannot mint payment-balance gems itself (there is no
// client-side HC reward impl), and the official balance moves exactly once per
// receive. The client gift carries only _masterRewardId; the server resolves it
// via MasterReward (here: a category-99 reward worth 200).
func TestGiftReceiveClientGiftCreditsGems(t *testing.T) {
	s := newSeededEconomyServer(t)
	xuid := fmt.Sprintf("client-gift-gem-%d", time.Now().UnixNano())
	userID, ok := s.resolvePlayer(context.Background(), xuid)
	if !ok {
		t.Fatal("resolve player failed")
	}
	pushFromCurrentState(t, s, xuid, map[string]any{
		"hdr":     "",
		"trigger": "RewardGranted",
		"deltas": map[string]any{
			"putItems": map[string]any{
				"user_client_gift": []any{map[string]any{
					"_giftId": 3, "_masterReasonId": 114000201, "_masterRewardId": 1000500152,
					"_isReceived": false, "_campaignRate": 10000, "_registeredAt": 1,
					"_startAt": 1, "_expiredAt": 9999999999, "_receivedAt": 0,
				}},
			},
		},
	})
	out := post(t, s, "/api/gift/receive", xuid, map[string]any{
		"hdr": "", "clientGiftIds": []any{3}, "serverGiftIds": []any{},
	})
	if out["code"].(json.Number).String() != "0" {
		t.Fatalf("gift receive failed: %v", out)
	}
	b, err := s.gems.Get(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Free != 200 || b.Paid != 0 {
		t.Fatalf("client gift gems = %+v, want free=200 paid=0", b)
	}
	// Receiving again is a no-op (already received) and must not double-credit.
	post(t, s, "/api/gift/receive", xuid, map[string]any{
		"hdr": "", "clientGiftIds": []any{3}, "serverGiftIds": []any{},
	})
	b, err = s.gems.Get(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Free != 200 || b.Paid != 0 {
		t.Fatalf("re-receive balance = %+v, want free=200 paid=0", b)
	}
}

// Client gifts with a term-stock reward get server-minted user_term_stock_item
// rows at receive time (uids are server-owned; the client adopts the returned
// rows). Reward 1000010219 is category 12 (StockItem.ticket.1) with num 2, so
// two single-value rows appear.
func TestGiftReceiveClientGiftMintsTermStock(t *testing.T) {
	s := newSeededEconomyServer(t)
	xuid := fmt.Sprintf("client-gift-ts-%d", time.Now().UnixNano())
	_, ok := s.resolvePlayer(context.Background(), xuid)
	if !ok {
		t.Fatal("resolve player failed")
	}
	pushFromCurrentState(t, s, xuid, map[string]any{
		"hdr":     "",
		"trigger": "RewardGranted",
		"deltas": map[string]any{
			"putItems": map[string]any{
				"user_client_gift": []any{map[string]any{
					"_giftId": 4, "_masterReasonId": 114000201, "_masterRewardId": 1000010219,
					"_isReceived": false, "_campaignRate": 10000, "_registeredAt": 1700000000,
					"_startAt": 1700000000, "_expiredAt": 9999999999, "_receivedAt": 0,
				}},
			},
		},
	})
	out := post(t, s, "/api/gift/receive", xuid, map[string]any{
		"hdr": "", "clientGiftIds": []any{4}, "serverGiftIds": []any{},
	})
	if out["code"].(json.Number).String() != "0" {
		t.Fatalf("gift receive failed: %v", out)
	}
	put := out["tables"].(map[string]any)["putItems"].(map[string]any)
	rows, ok := put["user_term_stock_item"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("expected 2 term-stock rows, got %v", put["user_term_stock_item"])
	}
	for _, r := range rows {
		row := r.(map[string]any)
		if row["_termStockItemId"].(json.Number).String() != "159000001" {
			t.Fatalf("wrong term stock item id: %v", row)
		}
		if row["_value"].(json.Number).String() != "1" || row["_uid"] == "" {
			t.Fatalf("bad term stock row: %v", row)
		}
		if row["_getTimestamp"].(json.Number).String() != "1700000000" {
			t.Fatalf("getTimestamp should mirror the gift's registeredAt: %v", row)
		}
	}
}

func TestDailyUpdateStampsProfileWithoutInventingStamina(t *testing.T) {
	s := newSeededEconomyServer(t)
	xuid := fmt.Sprintf("daily-%d", time.Now().UnixNano())
	out := post(t, s, "/api/daily/update", xuid, map[string]any{"hdr": "", "dummy": 0})
	put := out["tables"].(map[string]any)["putItems"].(map[string]any)
	if _, ok := put["user_profile"]; !ok {
		t.Fatalf("expected user_profile stamp, got %v", put)
	}
	// The seed now carries resource rows. Daily echoes them without refilling.
	for _, table := range []string{"user_life", "user_spirit"} {
		seed := model.NewPlayerSeed()[table]
		rows, ok := put[table].([]any)
		if len(seed) == 0 {
			if ok {
				t.Fatalf("invented %s", table)
			}
		} else if !ok || len(rows) != len(seed) || asInt(rows[0].(map[string]any)["_num"]) != asInt(seed[0]["_num"]) {
			t.Fatalf("daily changed seed resource %s", table)
		}
	}
	if _, ok := put["user_login_bonus"]; !ok {
		t.Fatalf("expected user_login_bonus, got %v", put)
	}
	if gifts, ok := put["user_server_gift"].([]any); !ok || len(gifts) == 0 {
		t.Fatalf("expected login-bonus gifts, got %v", put["user_server_gift"])
	}
}

func TestDailyUpdateEchoesExistingStamina(t *testing.T) {
	s := newSeededEconomyServer(t)
	xuid := fmt.Sprintf("daily-life-%d", time.Now().UnixNano())
	pushFromCurrentState(t, s, xuid, map[string]any{
		"hdr":     "",
		"trigger": "Test",
		"deltas": map[string]any{
			"putItems": map[string]any{
				"user_life": []any{map[string]any{
					"_num": 0, "_step": 0, "_completeRecoveredAt": int64(1),
					"_extraLifeElapsedTime": int64(7683753),
				}},
			},
		},
	})
	out := post(t, s, "/api/daily/update", xuid, map[string]any{"hdr": "", "dummy": 0})
	put := out["tables"].(map[string]any)["putItems"].(map[string]any)
	life := put["user_life"].([]any)[0].(map[string]any)
	if life["_num"].(json.Number).String() != "0" {
		t.Fatalf("daily must not refill depleted life: %v", life["_num"])
	}
	if life["_extraLifeElapsedTime"].(json.Number).String() != "7683753" {
		t.Fatalf("daily must keep extraLifeElapsedTime: %v", life["_extraLifeElapsedTime"])
	}
}

func TestMissionReceiveGrantsLimitBreakPower(t *testing.T) {
	s := newSeededEconomyServer(t)
	xuid := fmt.Sprintf("lbp-%d", time.Now().UnixNano())
	// mission 79210101 grants Reward.LimitBreakPower_200 (category 13, num 200).
	out := post(t, s, "/api/mission/receive", xuid, map[string]any{
		"hdr": "", "masterMissionIds": []any{79210101}, "enableSubscriptionBonus": false,
	})
	put := out["tables"].(map[string]any)["putItems"].(map[string]any)
	prof, ok := put["user_profile"].([]any)
	if !ok || len(prof) != 1 {
		t.Fatalf("expected user_profile in response, got %v", put["user_profile"])
	}
	if prof[0].(map[string]any)["_limitBreakPower"].(json.Number).String() != "200" {
		t.Fatalf("limit-break-power not granted: %v", prof[0].(map[string]any)["_limitBreakPower"])
	}
}

func TestGiftReceiveGrantsTermStock(t *testing.T) {
	s := newSeededEconomyServer(t)
	xuid := fmt.Sprintf("gift-term-%d", time.Now().UnixNano())
	pushFromCurrentState(t, s, xuid, map[string]any{
		"hdr":     "",
		"trigger": "Test",
		"deltas": map[string]any{
			"putItems": map[string]any{
				"user_server_gift": []any{map[string]any{
					"_giftId": 9001, "_rewardCategory": 12, "_itemId": 159000001,
					"_itemNum": 1, "_isReceived": false, "_receivedAt": 0,
				}},
			},
		},
	})
	out := post(t, s, "/api/gift/receive", xuid, map[string]any{
		"hdr": "", "clientGiftIds": []any{}, "serverGiftIds": []any{9001},
	})
	put := out["tables"].(map[string]any)["putItems"].(map[string]any)
	rows, ok := put["user_term_stock_item"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected 1 term-stock row, got %v", put["user_term_stock_item"])
	}
	row := rows[0].(map[string]any)
	if row["_termStockItemId"].(json.Number).String() != "159000001" {
		t.Fatalf("term stock id: %v", row["_termStockItemId"])
	}
	if row["_value"].(json.Number).String() != "1" {
		t.Fatalf("term stock value: %v", row["_value"])
	}
	uid, _ := row["_uid"].(string)
	if len(uid) != 32 {
		t.Fatalf("term stock uid: %q", uid)
	}
}
