// Package master loads decoded master game data (read-only, in RAM) and exposes
// the typed lookups the economy RPCs need. Each source file is {"items":[...]}.
// Master data is never written and never committed; it is supplied at runtime
// via the configured data directory.
package master

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Row is one master-data record.
type Row = map[string]any

// File is a loaded master file: its rows plus an id index.
type File struct {
	Name  string
	Items []Row
	ByID  map[int64]Row
}

// Data is the in-memory master-data set with gacha indices precomputed.
type Data struct {
	files map[string]*File

	lotteryByID   map[int64]Row   // MasterLottery: id -> row
	rateByGroup   map[int64][]Row // MasterLotteryRate: groupId -> rows
	rewardByGroup map[int64][]Row // MasterLotteryReward: groupId -> rows

	rewardByLabel map[string][]Row // MasterReward: groupLabel -> rows
	currencyByLbl map[string]int64 // MasterCurrency: label -> id
	itemByLbl     map[string]int64 // MasterItem: label -> id
	cardByLbl     map[string]int64 // MasterCard: label -> id
	cardConvert   map[int64]cardConvert
}

// cardConvert is the style-piece a duplicate gacha card converts into.
type cardConvert struct {
	Label  string
	ItemID int64
}

// Load reads every *.json master file under dir into memory and builds indices.
func Load(dir string) (*Data, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("master: read dir %s: %w", dir, err)
	}
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
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		f, err := loadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		d.files[name] = f
	}
	d.buildIndices()
	return d, nil
}

func loadFile(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("master: read %s: %w", path, err)
	}
	var doc struct {
		Items []Row `json:"items"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("master: parse %s: %w", path, err)
	}
	f := &File{Name: filepath.Base(path), Items: doc.Items, ByID: make(map[int64]Row, len(doc.Items))}
	for _, it := range doc.Items {
		if id, ok := num(it["id"]); ok {
			f.ByID[id] = it
		}
	}
	return f, nil
}

func (d *Data) buildIndices() {
	if f := d.files["MasterLottery"]; f != nil {
		d.lotteryByID = f.ByID
	}
	if f := d.files["MasterLotteryRate"]; f != nil {
		for _, r := range f.Items {
			if g, ok := num(r["groupId"]); ok {
				d.rateByGroup[g] = append(d.rateByGroup[g], r)
			}
		}
	}
	if f := d.files["MasterLotteryReward"]; f != nil {
		for _, r := range f.Items {
			if g, ok := num(r["groupId"]); ok {
				d.rewardByGroup[g] = append(d.rewardByGroup[g], r)
			}
		}
	}
	if f := d.files["MasterReward"]; f != nil {
		for _, r := range f.Items {
			if lbl, ok := r["groupLabel"].(string); ok && lbl != "" {
				d.rewardByLabel[lbl] = append(d.rewardByLabel[lbl], r)
			}
		}
	}
	if f := d.files["MasterCurrency"]; f != nil {
		for _, r := range f.Items {
			if lbl, ok := r["label"].(string); ok {
				if id, ok := num(r["id"]); ok {
					d.currencyByLbl[lbl] = id
				}
			}
		}
	}
	if f := d.files["MasterItem"]; f != nil {
		for _, r := range f.Items {
			if lbl, ok := r["label"].(string); ok && lbl != "" {
				if id, ok := num(r["id"]); ok {
					d.itemByLbl[lbl] = id
				}
			}
		}
	}
	if f := d.files["MasterCard"]; f != nil {
		if d.cardConvert == nil {
			d.cardConvert = map[int64]cardConvert{}
		}
		if d.cardByLbl == nil {
			d.cardByLbl = map[string]int64{}
		}
		for id, r := range f.ByID {
			if cl, _ := r["label"].(string); cl != "" {
				d.cardByLbl[cl] = id
			}
			lbl, _ := r["masterItemLabel"].(string)
			if lbl == "" {
				continue
			}
			itemID, ok := d.itemByLbl[lbl]
			if !ok {
				continue
			}
			d.cardConvert[id] = cardConvert{Label: lbl, ItemID: itemID}
		}
	}
}

// File returns a loaded master file by logical name (e.g. "MasterCard").
func (d *Data) File(name string) (*File, bool) {
	f, ok := d.files[name]
	return f, ok
}

// FileCount reports how many master files were loaded.
func (d *Data) FileCount() int { return len(d.files) }

// num coerces a JSON number/string into int64.
func num(v any) (int64, bool) {
	switch t := v.(type) {
	case float64:
		return int64(t), true
	case json.Number:
		n, err := t.Int64()
		return n, err == nil
	case int64:
		return t, true
	case int:
		return int64(t), true
	case string:
		if t == "" {
			return 0, false
		}
		n, err := strconv.ParseInt(t, 10, 64)
		return n, err == nil
	default:
		return 0, false
	}
}
