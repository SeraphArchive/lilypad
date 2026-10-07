package master

import "fmt"

// ItemLotteryDef is one MasterItemLottery row.
type ItemLotteryDef struct {
	ID                  int64
	RewardGroup         string
	ConsumeLabel        string
	ConsumeCount        int64
	DailyLimit          int64
	TotalLimit          int64
	DrawLimitPerRequest int64
	GuaranteedPickup    int64
	CompleteRewardGroup string
	AfterPickupComplete int64
}

// ItemLottery returns the item-lottery definition by id.
func (d *Data) ItemLottery(id int64) (ItemLotteryDef, bool) {
	if d == nil {
		return ItemLotteryDef{}, false
	}
	f, ok := d.File("MasterItemLottery")
	if !ok {
		return ItemLotteryDef{}, false
	}
	row, ok := f.ByID[id]
	if !ok {
		return ItemLotteryDef{}, false
	}
	var def ItemLotteryDef
	def.ID = id
	def.RewardGroup, _ = row["lotteryRewardGroupLabel"].(string)
	def.ConsumeLabel, _ = row["consumeItemLabel"].(string)
	def.ConsumeCount, _ = num(row["consumeItemCount"])
	def.DailyLimit, _ = num(row["dailyDrawLimit"])
	def.TotalLimit, _ = num(row["totalDrawLimit"])
	def.DrawLimitPerRequest, _ = num(row["drawLimitPerRequest"])
	def.GuaranteedPickup, _ = num(row["guaranteedPickupDrawCount"])
	def.CompleteRewardGroup, _ = row["completeRewardGroupLabel"].(string)
	def.AfterPickupComplete, _ = num(row["afterPickupCompleteType"])
	return def, true
}

// ItemLotteryPick is one weighted MasterItemLotteryReward hit.
type ItemLotteryPick struct {
	ID              int64
	RewardGroup     string
	IsPickup        bool
	DropLimit       int64
	MasterReward    []GroupReward
	CompleteRewards []GroupReward
}

type ItemLotteryRoll struct {
	Picks          []ItemLotteryPick
	DropCounts     map[int64]int64
	NonPickupCount int64
	ResetCount     int64
	Terminated     bool
}

// RollItemLottery draws n weighted rewards from the lottery's reward group,
// skipping entries that have already hit dropLimit (dropCounts is reward-id -> count).
func (d *Data) RollItemLottery(id int64, n int, dropCounts map[int64]int64, intn IntnFunc) ([]ItemLotteryPick, error) {
	result, err := d.RollItemLotteryState(id, n, dropCounts, 0, intn)
	return result.Picks, err
}

func (d *Data) RollItemLotteryState(id int64, n int, dropCounts map[int64]int64, nonPickup int64, intn IntnFunc) (ItemLotteryRoll, error) {
	result := ItemLotteryRoll{DropCounts: map[int64]int64{}, NonPickupCount: nonPickup}
	for id, count := range dropCounts {
		result.DropCounts[id] = count
	}
	def, ok := d.ItemLottery(id)
	if !ok {
		return result, fmt.Errorf("master: unknown item lottery %d", id)
	}
	if n <= 0 {
		n = 1
	}
	if n > MaxDrawCount || def.DrawLimitPerRequest > 0 && int64(n) > def.DrawLimitPerRequest {
		return result, fmt.Errorf("master: draw count exceeds limit")
	}
	f, ok := d.File("MasterItemLotteryReward")
	if !ok {
		return result, fmt.Errorf("master: no MasterItemLotteryReward")
	}
	var pool []Row
	for _, r := range f.Items {
		grp, _ := r["groupLabel"].(string)
		if grp != def.RewardGroup {
			continue
		}
		pool = append(pool, r)
	}
	if len(pool) == 0 {
		return result, fmt.Errorf("master: empty item-lottery pool %d", id)
	}
	if intn == nil {
		intn = func(n int) int { return 0 }
	}
	counts := result.DropCounts
	complete := func() bool {
		hasPickup := false
		for _, r := range pool {
			pickup, _ := r["isPickup"].(bool)
			if !pickup {
				continue
			}
			hasPickup = true
			id, _ := num(r["id"])
			limit, _ := num(r["dropLimit"])
			if limit <= 0 || counts[id] < limit {
				return false
			}
		}
		return hasPickup
	}
	wasComplete := complete()
	for i := 0; i < n; i++ {
		available := make([]Row, 0, len(pool))
		for _, r := range pool {
			id, _ := num(r["id"])
			limit, _ := num(r["dropLimit"])
			if limit <= 0 || counts[id] < limit {
				available = append(available, r)
			}
		}
		if len(available) == 0 {
			break
		}
		if def.GuaranteedPickup > 0 && result.NonPickupCount >= def.GuaranteedPickup-1 {
			var pickups []Row
			for _, r := range available {
				if pickup, _ := r["isPickup"].(bool); pickup {
					pickups = append(pickups, r)
				}
			}
			if len(pickups) > 0 {
				available = pickups
			}
		}
		total := int64(0)
		for _, r := range available {
			w, _ := num(r["ratio"])
			if w <= 0 {
				w = 1
			}
			total += w
		}
		pick := int64(intn(int(total)))
		chosen := available[len(available)-1]
		for _, r := range available {
			w, _ := num(r["ratio"])
			if w <= 0 {
				w = 1
			}
			if pick < w {
				chosen = r
				break
			}
			pick -= w
		}
		rid, _ := num(chosen["id"])
		counts[rid]++
		rg, _ := chosen["rewardGroupLabel"].(string)
		pickup, _ := chosen["isPickup"].(bool)
		if pickup {
			result.NonPickupCount = 0
		} else {
			result.NonPickupCount++
		}
		limit, _ := num(chosen["dropLimit"])
		pickResult := ItemLotteryPick{
			ID: rid, RewardGroup: rg, IsPickup: pickup, DropLimit: limit,
			MasterReward: d.RewardGroup(rg),
		}
		if len(pickResult.MasterReward) == 0 {
			return result, fmt.Errorf("master: missing item-lottery reward group %q", rg)
		}
		completed := !wasComplete && complete()
		if completed {
			pickResult.CompleteRewards = d.RewardGroup(def.CompleteRewardGroup)
			if def.CompleteRewardGroup != "" && len(pickResult.CompleteRewards) == 0 {
				return result, fmt.Errorf("master: missing item-lottery completion group %q", def.CompleteRewardGroup)
			}
		}
		result.Picks = append(result.Picks, pickResult)
		if completed {
			switch def.AfterPickupComplete {
			case 1:
				result.Terminated = true
				return result, nil
			case 2:
				for _, r := range pool {
					id, _ := num(r["id"])
					counts[id] = 0
				}
				result.NonPickupCount = 0
				result.ResetCount++
				wasComplete = false
			default:
				wasComplete = true
			}
		}
	}
	if len(result.Picks) == 0 {
		return result, fmt.Errorf("master: item lottery exhausted")
	}
	return result, nil
}

// SelectTicketLotteryDef is one MasterSelectTicketLottery row.
type SelectTicketLotteryDef struct {
	ID                     int64
	ShopID                 int64
	ConsumeTermStockItemID int64
	TermStockItemCost      int64
	LimitDrawCount         int64
}

// SelectTicketLottery returns the select-ticket lottery definition.
func (d *Data) SelectTicketLottery(id int64) (SelectTicketLotteryDef, bool) {
	if d == nil {
		return SelectTicketLotteryDef{}, false
	}
	f, ok := d.File("MasterSelectTicketLottery")
	if !ok {
		return SelectTicketLotteryDef{}, false
	}
	row, ok := f.ByID[id]
	if !ok {
		return SelectTicketLotteryDef{}, false
	}
	var def SelectTicketLotteryDef
	def.ID = id
	def.ShopID, _ = num(row["masterLotteryShopId"])
	def.ConsumeTermStockItemID, _ = num(row["consumeTermStockItemId"])
	def.TermStockItemCost, _ = num(row["termStockItemCost"])
	def.LimitDrawCount, _ = num(row["limitDrawCount"])
	if def.TermStockItemCost <= 0 {
		def.TermStockItemCost = 1
	}
	return def, true
}

// SelectTicketRewardCard resolves a MasterSelectTicketLotteryReward id to a card id.
func (d *Data) SelectTicketRewardCard(rewardID int64) (cardID int64, ok bool) {
	if d == nil {
		return 0, false
	}
	f, ok := d.File("MasterSelectTicketLotteryReward")
	if !ok {
		return 0, false
	}
	row, ok := f.ByID[rewardID]
	if !ok {
		return 0, false
	}
	lbl, _ := row["rewardCardLabel"].(string)
	return d.CardID(lbl)
}

// SelectTicketRewardByCard finds a reward id for lottery whose card matches cardID.
func (d *Data) SelectTicketRewardByCard(cardID int64) (rewardID int64, ok bool) {
	if d == nil || cardID == 0 {
		return 0, false
	}
	f, ok := d.File("MasterSelectTicketLotteryReward")
	if !ok {
		return 0, false
	}
	want := ""
	for lbl, id := range d.cardByLbl {
		if id == cardID {
			want = lbl
			break
		}
	}
	if want == "" {
		return 0, false
	}
	for _, r := range f.Items {
		if lbl, _ := r["rewardCardLabel"].(string); lbl == want {
			id, ok := num(r["id"])
			return id, ok
		}
	}
	return 0, false
}
