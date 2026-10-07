package master

// RewardByID resolves one MasterReward row by id (the _masterRewardId carried
// by gift rows). Returns the category, num, and masterLabel.
func (d *Data) RewardByID(id int64) (category, numOut int64, masterLabel string, ok bool) {
	if d == nil {
		return 0, 0, "", false
	}
	f, found := d.files["MasterReward"]
	if !found {
		return 0, 0, "", false
	}
	row, found := f.ByID[id]
	if !found {
		return 0, 0, "", false
	}
	category, _ = num(row["category"])
	numOut, _ = num(row["num"])
	masterLabel, _ = row["masterLabel"].(string)
	return category, numOut, masterLabel, true
}

// GroupReward is one reward entry within a labeled reward group (MasterReward).
type GroupReward struct {
	ID          int64
	Category    int64
	Num         int64
	MasterLabel string // item/currency/character label, "" for bare-num rewards (e.g. gems)
}

// RewardGroup returns the rewards in a labeled group (e.g. a mission's
// masterRewardGroupLabel or a login-bonus content's rewardGroupLabel).
func (d *Data) RewardGroup(label string) []GroupReward {
	rows := d.rewardByLabel[label]
	out := make([]GroupReward, 0, len(rows))
	for _, r := range rows {
		id, _ := num(r["id"])
		cat, _ := num(r["category"])
		n, _ := num(r["num"])
		lbl, _ := r["masterLabel"].(string)
		out = append(out, GroupReward{ID: id, Category: cat, Num: n, MasterLabel: lbl})
	}
	return out
}

// CurrencyID resolves a currency label (e.g. "item_GP") to its currency id.
func (d *Data) CurrencyID(label string) (int64, bool) {
	id, ok := d.currencyByLbl[label]
	return id, ok
}

// ItemID resolves an item label (e.g. "PrismBattleTicket") to its item id.
func (d *Data) ItemID(label string) (int64, bool) {
	id, ok := d.itemByLbl[label]
	return id, ok
}

// MissionRewardGroup returns a mission's reward-group label.
func (d *Data) MissionRewardGroup(missionID int64) (string, bool) {
	f, ok := d.files["MasterMission"]
	if !ok {
		return "", false
	}
	row, ok := f.ByID[missionID]
	if !ok {
		return "", false
	}
	lbl, ok := row["masterRewardGroupLabel"].(string)
	return lbl, ok && lbl != ""
}

// CurrencyGrants resolves a reward group to currency deltas (currencyID -> added
// amount), using the masterLabel->currency mapping. Non-currency rewards (items,
// gems, cards, …) are ignored — those are granted by other paths.
func (d *Data) CurrencyGrants(groupLabel string) map[int64]int64 {
	cur, _ := d.RewardGrants(groupLabel)
	return cur
}

// profileFieldByCategory maps reward categories that accumulate onto a
// user_profile field. Category 13 is limit-break power (groupLabel
// "Reward.LimitBreakPower_N", bare num).
var profileFieldByCategory = map[int64]string{
	13: "_limitBreakPower",
}

// ProfileGrants resolves a reward group to user_profile field deltas
// (field name -> added amount) for the reward categories that target the profile.
func (d *Data) ProfileGrants(groupLabel string) map[string]int64 {
	out := map[string]int64{}
	for _, r := range d.RewardGroup(groupLabel) {
		if field, ok := profileFieldByCategory[r.Category]; ok {
			out[field] += r.Num
		}
	}
	return out
}

// keyed by currency id and item id respectively. A reward's masterLabel is
// matched first against MasterCurrency, then MasterItem; rewards that match
// neither (bare-num gems, cards, accessories, stock items, …) are left for
// other granting paths.
func (d *Data) RewardGrants(groupLabel string) (currencies, items map[int64]int64) {
	currencies = map[int64]int64{}
	items = map[int64]int64{}
	for _, r := range d.RewardGroup(groupLabel) {
		if r.MasterLabel == "" {
			continue
		}
		if id, ok := d.CurrencyID(r.MasterLabel); ok {
			currencies[id] += r.Num
			continue
		}
		if id, ok := d.ItemID(r.MasterLabel); ok {
			items[id] += r.Num
		}
	}
	return currencies, items
}

// RewardCategoryHardCurrency (HC) is the gacha gem / quartz reward category. It
// is a bare-num reward (no masterLabel), so RewardGrants skips it — GemGrants is
// the dedicated accessor.
const RewardCategoryHardCurrency = 99

// GemGrants sums the gem (hard-currency, category 99) num awarded by a reward group.
func (d *Data) GemGrants(groupLabel string) int64 {
	var total int64
	for _, r := range d.RewardGroup(groupLabel) {
		if r.Category == RewardCategoryHardCurrency {
			total += r.Num
		}
	}
	return total
}

// InviteSenderConditionIDs resolves a MasterInvite id to its sender-side
// MasterInviteCondition ids (via the invite's senderConditionGroupLabel), in
// master order.
func (d *Data) InviteSenderConditionIDs(inviteID int64) ([]int64, bool) {
	if d == nil {
		return nil, false
	}
	invF, ok := d.files["MasterInvite"]
	if !ok {
		return nil, false
	}
	inv, ok := invF.ByID[inviteID]
	if !ok {
		return nil, false
	}
	group, _ := inv["senderConditionGroupLabel"].(string)
	if group == "" {
		return nil, false
	}
	condF, ok := d.files["MasterInviteCondition"]
	if !ok {
		return nil, false
	}
	var ids []int64
	for _, r := range condF.Items {
		if g, _ := r["groupLabel"].(string); g != group {
			continue
		}
		if id, ok := num(r["id"]); ok {
			ids = append(ids, id)
		}
	}
	return ids, len(ids) > 0
}

// RewardByLabel resolves one MasterReward row by its (unique) label — the
// reference used by MasterStockableRegularReward.rewardLabel.
func (d *Data) RewardByLabel(label string) (Row, bool) {
	if d == nil || label == "" {
		return nil, false
	}
	f, ok := d.files["MasterReward"]
	if !ok {
		return nil, false
	}
	for _, r := range f.Items {
		if lbl, _ := r["label"].(string); lbl == label {
			return r, true
		}
	}
	return nil, false
}

// StockableRegularReward is one MasterStockableRegularReward row (a periodic
// stockable reward, e.g. the weekly 異時層 ticket).
type StockableRegularReward struct {
	ID                  int64
	Category            int64
	RewardLabel         string
	StockPeriodLabel    string
	ResetTermType       int64
	MaxRewardNumPerTerm int64
	MaxStockNum         int64
	ReleaseLabel        string
}

// StockableRegularRewards returns the MasterStockableRegularReward rows for a
// category (the receive request's `category` field).
func (d *Data) StockableRegularRewards(category int64) []StockableRegularReward {
	if d == nil {
		return nil
	}
	f, ok := d.files["MasterStockableRegularReward"]
	if !ok {
		return nil
	}
	var out []StockableRegularReward
	for _, r := range f.Items {
		cat, _ := num(r["category"])
		if cat != category {
			continue
		}
		var srr StockableRegularReward
		srr.ID, _ = num(r["id"])
		srr.Category = cat
		srr.RewardLabel, _ = r["rewardLabel"].(string)
		srr.StockPeriodLabel, _ = r["stockPeriod"].(string)
		srr.ResetTermType, _ = num(r["resetTermType"])
		srr.MaxRewardNumPerTerm, _ = num(r["maxRewardNumPerTerm"])
		srr.MaxStockNum, _ = num(r["maxStockNum"])
		srr.ReleaseLabel, _ = r["releaseLabel"].(string)
		out = append(out, srr)
	}
	return out
}
