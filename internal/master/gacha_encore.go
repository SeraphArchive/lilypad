package master

// encoreShopLabels preserves the known legacy link when decoded master data
// does not supply MasterLotteryShop.masterLotteryEncoreLabel.
var encoreShopLabels = map[int64]string{
	65001544: "lotteryEncore_1",
}

// EncoreDef is one MasterLotteryEncore row resolved for a lottery shop.
type EncoreDef struct {
	ShopID           int64
	Label            string
	ScenarioGroup    string
	GodRate          int64
	GodScenarioGroup string
	LimitDrawDay     int64
	ReleaseLabel     string
}

// EncoreForShop resolves a lottery shop to its encore definition. ok is false
// when the shop is not an encore shop or the encore master data is absent.
func (d *Data) EncoreForShop(shopID int64) (EncoreDef, bool) {
	if d == nil {
		return EncoreDef{}, false
	}
	label := encoreShopLabels[shopID]
	if shops, ok := d.File("MasterLotteryShop"); ok {
		if linked, _ := shops.ByID[shopID]["masterLotteryEncoreLabel"].(string); linked != "" {
			label = linked
		}
	}
	if label == "" {
		return EncoreDef{}, false
	}
	f, ok := d.File("MasterLotteryEncore")
	if !ok {
		return EncoreDef{}, false
	}
	for _, r := range f.Items {
		if s, _ := r["label"].(string); s != label {
			continue
		}
		return EncoreDef{
			ShopID:           shopID,
			Label:            label,
			ScenarioGroup:    strField(r, "scenarioGroupLabel"),
			GodRate:          numField(r, "godRate"),
			GodScenarioGroup: strField(r, "godScenarioGroupLabel"),
			LimitDrawDay:     numField(r, "limitDrawDay"),
			ReleaseLabel:     strField(r, "releaseLabel"),
		}, true
	}
	return EncoreDef{}, false
}

// EncoreScenario is one MasterLotteryEncoreScenario row (group + weighted rate).
type EncoreScenario struct {
	ID    int64
	Label string
	Group string
	Rate  int64
}

// EncoreScenarios returns the scenarios in a group (groupLabel), in file order.
func (d *Data) EncoreScenarios(groupLabel string) []EncoreScenario {
	f, ok := d.File("MasterLotteryEncoreScenario")
	if !ok {
		return nil
	}
	var out []EncoreScenario
	for _, r := range f.Items {
		if s, _ := r["groupLabel"].(string); s != groupLabel {
			continue
		}
		id, _ := num(r["id"])
		lbl, _ := r["label"].(string)
		rate, _ := num(r["rate"])
		out = append(out, EncoreScenario{ID: id, Label: lbl, Group: groupLabel, Rate: rate})
	}
	return out
}

// PickEncoreScenario selects the scenario for a fresh encore draw day: a god
// roll (GodRate out of 10000) then a weighted roll over the group's scenarios
// by rate. It returns the scenario id and whether it was a god draw.
func (d *Data) PickEncoreScenario(enc EncoreDef, intn IntnFunc) (int64, bool) {
	if intn == nil {
		intn = func(n int) int { return 0 }
	}
	if enc.GodRate > 0 {
		if god := d.EncoreScenarios(enc.GodScenarioGroup); len(god) > 0 && intn(10000) < int(enc.GodRate) {
			return god[0].ID, true
		}
	}
	scens := d.EncoreScenarios(enc.ScenarioGroup)
	if len(scens) == 0 {
		return 0, false
	}
	total := int64(0)
	for _, s := range scens {
		if s.Rate > 0 {
			total += s.Rate
		}
	}
	if total <= 0 {
		return scens[0].ID, false
	}
	pick := int64(intn(int(total)))
	for _, s := range scens {
		if s.Rate <= 0 {
			continue
		}
		if pick < s.Rate {
			return s.ID, false
		}
		pick -= s.Rate
	}
	return scens[len(scens)-1].ID, false
}

// EncoreShops returns encore definitions for the given lottery-shop ids that
// are currently open (MasterRelease).
func (d *Data) EncoreShops(shopIDs []int, now int64) []EncoreDef {
	if d == nil {
		return nil
	}
	var out []EncoreDef
	for _, id := range shopIDs {
		enc, ok := d.EncoreForShop(int64(id))
		if !ok {
			continue
		}
		if enc.ReleaseLabel != "" && !d.IsOpened(enc.ReleaseLabel, now) {
			continue
		}
		out = append(out, enc)
	}
	return out
}

func strField(r Row, k string) string {
	s, _ := r[k].(string)
	return s
}

func numField(r Row, k string) int64 {
	n, _ := num(r[k])
	return n
}
