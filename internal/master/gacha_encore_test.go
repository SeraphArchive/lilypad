package master

import "testing"

func syntheticEncore() *Data {
	d := &Data{files: map[string]*File{}}
	d.files["MasterLotteryEncore"] = &File{Items: []Row{
		{"id": 321000001, "label": "lotteryEncore_1",
			"scenarioGroupLabel": "lotteryEncoreScenario_group1", "godRate": 800,
			"godScenarioGroupLabel": "lotteryEncoreScenario_godGroup1",
			"limitDrawDay":          10, "releaseLabel": "release.FreeGachaFestival"},
	}}
	d.files["MasterLotteryEncoreScenario"] = &File{Items: []Row{
		{"id": 323000001, "label": "lotteryEncoreScenario_1", "groupLabel": "lotteryEncoreScenario_group1", "rate": 100},
		{"id": 323000002, "label": "lotteryEncoreScenario_2", "groupLabel": "lotteryEncoreScenario_group1", "rate": 100},
		{"id": 323001001, "label": "lotteryEncoreScenario_1001", "groupLabel": "lotteryEncoreScenario_godGroup1", "rate": 0},
	}}
	return d
}

func TestEncoreForShop(t *testing.T) {
	d := syntheticEncore()
	enc, ok := d.EncoreForShop(65001544)
	if !ok || enc.LimitDrawDay != 10 || enc.ScenarioGroup != "lotteryEncoreScenario_group1" ||
		enc.GodRate != 800 || enc.GodScenarioGroup != "lotteryEncoreScenario_godGroup1" {
		t.Fatalf("EncoreForShop = %+v %v", enc, ok)
	}
	if _, ok := d.EncoreForShop(12345); ok {
		t.Fatal("non-encore shop must not resolve")
	}
}

func TestPickEncoreScenario(t *testing.T) {
	d := syntheticEncore()
	enc, _ := d.EncoreForShop(65001544)

	// god roll: intn(10000)=0 < 800 → god scenario 323001001.
	id, isGod := d.PickEncoreScenario(enc, func(n int) int { return 0 })
	if !isGod || id != 323001001 {
		t.Fatalf("god scenario = %d %v", id, isGod)
	}

	// regular roll: intn(10000)=1000 (>= 800), then intn(200)=0 → first scenario.
	id, isGod = d.PickEncoreScenario(enc, func(n int) int {
		if n == 10000 {
			return 1000
		}
		return 0
	})
	if isGod || id != 323000001 {
		t.Fatalf("regular scenario = %d %v", id, isGod)
	}
}

func TestEncoreForShopUsesMasterLinkAndKeepsLegacyFallback(t *testing.T) {
	d := syntheticEncore()
	d.files["MasterLotteryEncore"].Items = append(d.files["MasterLotteryEncore"].Items,
		Row{"label": "encore.next", "scenarioGroupLabel": "scenario.next", "limitDrawDay": 5})
	d.files["MasterLotteryShop"] = &File{ByID: map[int64]Row{
		65000001: {"masterLotteryEncoreLabel": "encore.next"},
		65001544: {"masterLotteryEncoreLabel": "encore.next"},
	}}
	for _, id := range []int64{65000001, 65001544} {
		enc, ok := d.EncoreForShop(id)
		if !ok || enc.Label != "encore.next" || enc.ScenarioGroup != "scenario.next" || enc.LimitDrawDay != 5 {
			t.Fatalf("master link for %d = %+v, %v", id, enc, ok)
		}
	}
	delete(d.files["MasterLotteryShop"].ByID[65001544], "masterLotteryEncoreLabel")
	if enc, ok := d.EncoreForShop(65001544); !ok || enc.Label != "lotteryEncore_1" {
		t.Fatalf("legacy link = %+v, %v", enc, ok)
	}
	d.files["MasterLotteryShop"].ByID[65001544]["masterLotteryEncoreLabel"] = "encore.missing"
	if _, ok := d.EncoreForShop(65001544); ok {
		t.Fatal("unresolved explicit master link used the legacy event")
	}
}
