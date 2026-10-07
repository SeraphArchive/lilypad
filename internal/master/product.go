package master

import (
	"fmt"
	"strconv"
	"strings"
)

// PlatformSteam is MasterRelease.platform for the Steam / PC client.
const PlatformSteam = 4

// ProductSKU is one Gree payment/productlist entry. Numeric fields are strings
// because that is the official JSON shape.
type ProductSKU struct {
	ProductID      string
	Name           string
	Description    string
	ChargeGem      int64
	FreeGem        int64
	BonusGem       int64
	IncreaseGem    int64
	CampaignType   int
	CampaignMode   int
	Priority       int
	Consumable     int
	LimitedCount   int
	ProductType    int
	Price          int64
	FormattedPrice string
	Currency       string
}

// DefaultSteamGemSKUs is the evergreen Steam quartz ladder (JPY), taken from
// official productlist traffic. MasterGamelibProduct has the ids/names but not
// the store price, so the yen amounts live here.
var DefaultSteamGemSKUs = []ProductSKU{
	{ProductID: "115093001", ChargeGem: 130, Price: 160, Priority: 1, Consumable: 1},
	{ProductID: "115093002", ChargeGem: 410, Price: 480, Priority: 2, Consumable: 1},
	{ProductID: "115093003", ChargeGem: 870, Price: 1000, Priority: 3, Consumable: 1},
	{ProductID: "115093004", ChargeGem: 1370, Price: 1500, Priority: 4, Consumable: 1},
	{ProductID: "115093005", ChargeGem: 2720, Price: 2900, Priority: 5, Consumable: 1},
	{ProductID: "115093006", ChargeGem: 4750, Price: 4900, Priority: 6, Consumable: 1},
	{ProductID: "115093007", ChargeGem: 10000, Price: 10000, Priority: 7, Consumable: 1},
	{ProductID: "115093008", ChargeGem: 3000, Price: 2900, Priority: 8, Consumable: 0},
	{ProductID: "115093009", ChargeGem: 6000, Price: 4900, Priority: 9, Consumable: 0},
	{ProductID: "115093010", ChargeGem: 15000, Price: 10000, Priority: 10, Consumable: 0},
}

// ProductList returns the Gree productlist payload for now. When master data is
// present, SKU ids are filtered by MasterGamelibProduct.releaseLabel.
func (d *Data) ProductList(now int64) []map[string]any {
	out := make([]map[string]any, 0, len(DefaultSteamGemSKUs))
	for _, sku := range DefaultSteamGemSKUs {
		if d != nil {
			if row, ok := d.gamelibProduct(sku.ProductID); ok {
				rel, _ := row["releaseLabel"].(string)
				if !d.IsOpenedFor(rel, now, PlatformSteam, 0) {
					continue
				}
				if name, _ := row["name"].(string); name != "" {
					sku.Name = name
				}
				if desc, _ := row["description"].(string); desc != "" {
					sku.Description = strings.TrimPrefix(desc, "・")
				}
			}
		}
		if sku.Name == "" {
			sku.Name = fmt.Sprintf("クォーツ × %d", sku.ChargeGem)
		}
		if sku.Description == "" {
			sku.Description = sku.Name
		}
		sku.FormattedPrice = formatYen(sku.Price)
		sku.Currency = "JPY"
		sku.FreeGem, sku.BonusGem, sku.IncreaseGem = 0, 0, 0
		out = append(out, sku.Map())
	}
	return out
}

func (d *Data) gamelibProduct(id string) (Row, bool) {
	f, ok := d.File("MasterGamelibProduct")
	if !ok {
		return nil, false
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, false
	}
	row, ok := f.ByID[n]
	return row, ok
}

func (s ProductSKU) Map() map[string]any {
	return map[string]any{
		"product_id":      s.ProductID,
		"name":            s.Name,
		"description":     s.Description,
		"thumbnail_url":   "",
		"charge_gem":      itoa(s.ChargeGem),
		"free_gem":        itoa(s.FreeGem),
		"increase_gem":    itoa(s.IncreaseGem),
		"bonus_gem":       itoa(s.BonusGem),
		"total_gem":       itoa(s.ChargeGem + s.FreeGem + s.BonusGem),
		"campaign_type":   itoa(int64(s.CampaignType)),
		"campaign_mode":   itoa(int64(s.CampaignMode)),
		"priority":        itoa(int64(s.Priority)),
		"consumable":      itoa(int64(s.Consumable)),
		"limited_count":   itoa(int64(s.LimitedCount)),
		"product_type":    itoa(int64(s.ProductType)),
		"price":           itoa(s.Price),
		"formatted_price": s.FormattedPrice,
		"currency":        s.Currency,
	}
}

func formatYen(n int64) string {
	s := strconv.FormatInt(n, 10)
	if n < 1000 {
		return "¥" + s
	}
	var b strings.Builder
	b.WriteString("¥")
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
