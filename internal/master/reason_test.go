package master

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoginBonusReasonResolvesLabelNotRewardID(t *testing.T) {
	dir := t.TempDir()
	defs := map[string][]map[string]any{
		"MasterReason": {
			{"id": 800, "label": "reason.default"},
			{"id": 801, "label": "reason.synthetic.login"},
		},
		"MasterItem": {{"id": 700, "label": "item.synthetic"}},
		"MasterReward": {
			{"id": 900, "groupLabel": "login", "category": 99, "num": 20, "masterReasonLabel": "reason.synthetic.login"},
			{"id": 901, "groupLabel": "login", "category": 99, "num": 50, "masterReasonLabel": ""},
			{"id": 902, "groupLabel": "login", "category": 1, "num": 500, "masterLabel": "item.synthetic", "masterReasonLabel": "reason.synthetic.login"},
		},
	}
	for name, items := range defs {
		raw, err := json.Marshal(map[string]any{"items": items})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	md, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	rewards := md.LoginBonusRewards("login")
	if len(rewards) != 3 {
		t.Fatal("missing login rewards", rewards)
	}
	for i, want := range []int64{801, 0, 801} {
		if rewards[i].ReasonID != want || rewards[i].ID != int64(900+i) {
			t.Fatalf("reward %d used the wrong reason: %+v; want reason %d", i, rewards[i], want)
		}
	}
	if rewards[2].ItemID != 700 || rewards[2].Num != 500 {
		t.Fatal("shared reason changed the reward payload", rewards[2])
	}
}
