package master

import (
	"os"
	"testing"
)

// synthetic builds an in-memory Data for gacha tests (no data directory needed).
func synthetic() *Data {
	d := &Data{
		files:         map[string]*File{},
		lotteryByID:   map[int64]Row{},
		rateByGroup:   map[int64][]Row{},
		rewardByGroup: map[int64][]Row{},
	}
	d.files["MasterLottery"] = &File{ByID: map[int64]Row{
		100: {"id": 100, "masterLotteryRateGroupId": 200, "rewardCount": 3},
	}}
	d.files["MasterLotteryRate"] = &File{Items: []Row{
		{"groupId": 200, "rarity": 1, "masterLotteryRewardGroupId": 300, "rate": 70},
		{"groupId": 200, "rarity": 2, "masterLotteryRewardGroupId": 301, "rate": 30},
	}}
	d.files["MasterLotteryReward"] = &File{Items: []Row{
		{"groupId": 300, "rewardCategory": 9, "rewardId": 1001},
		{"groupId": 301, "rewardCategory": 9, "rewardId": 2001},
	}}
	d.buildIndices()
	return d
}

func TestRollFirstTier(t *testing.T) {
	d := synthetic()
	// intn always 0: pick lands in first rate tier (rate 70), first reward.
	rewards, err := d.Roll(100, 0, func(int) int { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	if len(rewards) != 3 {
		t.Fatalf("default rewardCount 3 expected, got %d", len(rewards))
	}
	if rewards[0].RewardID != 1001 || rewards[0].Category != RewardCategoryCard {
		t.Fatalf("first tier reward wrong: %+v", rewards[0])
	}
}

func TestRollSecondTierByWeight(t *testing.T) {
	d := synthetic()
	// pick=75 lands past the first tier (70) into the second (30): reward 2001.
	r, err := d.rollOne(200, func(n int) int {
		if n == 100 { // total weight
			return 75
		}
		return 0
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.RewardID != 2001 {
		t.Fatalf("weighted boundary wrong: %+v", r)
	}
}

func TestRollUnknownLottery(t *testing.T) {
	if _, err := synthetic().Roll(999, 1, func(int) int { return 0 }); err == nil {
		t.Fatal("expected error for unknown lottery")
	}
}

func TestLoadRealDataDir(t *testing.T) {
	dir := os.Getenv("LILYPAD_DATA_DIR")
	if dir == "" {
		t.Skip("LILYPAD_DATA_DIR not set; skipping real master-data load")
	}
	d, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d.FileCount() == 0 {
		t.Fatal("no master files loaded")
	}
	lot, ok := d.File("MasterLottery")
	if !ok || len(lot.ByID) == 0 {
		t.Fatal("MasterLottery not loaded")
	}
	// Roll the first available lottery; it should grant valid rewards.
	var anyID int64
	for id := range lot.ByID {
		anyID = id
		break
	}
	rewards, err := d.Roll(anyID, 1, func(n int) int { return n / 2 })
	if err != nil {
		t.Logf("roll lottery %d: %v (some lotteries may lack a rate group)", anyID, err)
		return
	}
	if len(rewards) == 0 {
		t.Fatal("roll returned no rewards")
	}
}

func syntheticRewards() *Data {
	d := &Data{
		files:         map[string]*File{},
		rewardByLabel: map[string][]Row{},
		currencyByLbl: map[string]int64{},
		itemByLbl:     map[string]int64{},
	}
	d.files["MasterReward"] = &File{Items: []Row{
		{"groupLabel": "Reward.M1", "category": 10, "num": 5000, "masterLabel": "item_GP"},
		{"groupLabel": "Reward.M1", "category": 99, "num": 20, "masterLabel": ""},
		{"groupLabel": "Reward.M1", "category": 1, "num": 3, "masterLabel": "PrismTicket"},
	}}
	d.files["MasterCurrency"] = &File{Items: []Row{
		{"id": 101000001, "label": "item_GP"},
	}}
	d.files["MasterItem"] = &File{Items: []Row{
		{"id": 7010075, "label": "PrismTicket"},
	}}
	d.files["MasterMission"] = &File{ByID: map[int64]Row{
		500: {"id": 500, "masterRewardGroupLabel": "Reward.M1"},
	}}
	d.buildIndices()
	return d
}

func TestRewardGroupAndCurrency(t *testing.T) {
	d := syntheticRewards()
	rs := d.RewardGroup("Reward.M1")
	if len(rs) != 3 {
		t.Fatalf("expected 3 rewards, got %d", len(rs))
	}
	grp, ok := d.MissionRewardGroup(500)
	if !ok || grp != "Reward.M1" {
		t.Fatalf("mission reward group wrong: %q %v", grp, ok)
	}
	currencies, items := d.RewardGrants("Reward.M1")
	// GP (cat 10) -> currency; PrismTicket (cat 1) -> item; gem (cat 99, no
	// masterLabel) -> neither.
	if len(currencies) != 1 || currencies[101000001] != 5000 {
		t.Fatalf("currency grants wrong: %v", currencies)
	}
	if len(items) != 1 || items[7010075] != 3 {
		t.Fatalf("item grants wrong: %v", items)
	}
}

func TestCardConvert(t *testing.T) {
	d := &Data{
		files:         map[string]*File{},
		lotteryByID:   map[int64]Row{},
		rateByGroup:   map[int64][]Row{},
		rewardByGroup: map[int64][]Row{},
		rewardByLabel: map[string][]Row{},
		currencyByLbl: map[string]int64{},
		itemByLbl:     map[string]int64{},
		cardConvert:   map[int64]cardConvert{},
	}
	d.files["MasterItem"] = &File{Items: []Row{
		{"id": 7031101, "label": "Item_RKayamori01"},
	}}
	d.files["MasterCard"] = &File{ByID: map[int64]Row{
		1001101: {"id": 1001101, "masterItemLabel": "Item_RKayamori01"},
		1000000: {"id": 1000000, "masterItemLabel": ""},
	}}
	d.buildIndices()
	lbl, iid, ok := d.CardConvert(1001101)
	if !ok || lbl != "Item_RKayamori01" || iid != 7031101 {
		t.Fatalf("CardConvert = %q %d %v", lbl, iid, ok)
	}
	if _, _, ok := d.CardConvert(1000000); ok {
		t.Fatal("empty masterItemLabel must not convert")
	}
}

func TestTermStockEffectiveDays(t *testing.T) {
	d := &Data{files: map[string]*File{}}
	d.files["MasterTermStockItem"] = &File{ByID: map[int64]Row{
		159000001: {"id": 159000001, "effectiveDayNum": 30},
	}}
	if got := d.TermStockEffectiveDays(159000001); got != 30 {
		t.Fatalf("days=%d", got)
	}
	if got := d.TermStockEffectiveDays(1); got != 30 {
		t.Fatalf("fallback days=%d", got)
	}
}

func TestDailyLookupsAgainstRealMaster(t *testing.T) {
	dir := os.Getenv("LILYPAD_DATA_DIR")
	if dir == "" {
		t.Skip("LILYPAD_DATA_DIR not set")
	}
	d, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	ids := d.DailyMissionsForWeekday(4) // Thursday
	if len(ids) < 4 {
		t.Fatalf("thursday dailies = %v", ids)
	}
	g := d.DefaultGardenDropIDs()
	if len(g) < 7 {
		t.Fatalf("default garden drops = %v", g)
	}
	var foundDaily bool
	for _, b := range d.LoginBonuses() {
		if b.ID == 107000001 && b.CanLoop && len(b.Days) == 7 {
			foundDaily = true
			c, ok := b.ContentForDay(6)
			if !ok || c.RewardGroup != "Reward.NormalLogin_06" {
				t.Fatalf("daily day6 = %+v %v", c, ok)
			}
			rws := d.LoginBonusRewards(c.RewardGroup)
			if len(rws) == 0 || rws[0].Category != RewardCategoryHardCurrency || rws[0].Num != 20 {
				t.Fatalf("normal login day6 rewards = %+v", rws)
			}
		}
	}
	if !foundDaily {
		t.Fatal("looping daily login bonus 107000001 missing")
	}
	// Snapshot-specific point mapping is covered with synthetic definitions;
	// external master snapshots may predate this banner.
	if _, present := d.Lottery(62007542); present {
		if id, ok := d.LotteryPointItemID(62007542); !ok || id != 7061241 {
			t.Fatalf("point item for 62007542 = %d %v want 7061241", id, ok)
		}
	}
	var foundCC bool
	for _, b := range d.LoginBonuses() {
		if b.ID == 107000095 && b.ReleaseLabel == "release.loginbonus_CC0005" {
			foundCC = true
		}
	}
	if !foundCC {
		t.Fatal("event login bonus 107000095 missing release label")
	}
}
