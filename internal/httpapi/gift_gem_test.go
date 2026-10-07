package httpapi

import (
	"testing"

	"lilypad/internal/deltacomm"
)

func TestGiftGemAmountAcceptsItemNumAndBareNum(t *testing.T) {
	tests := []struct {
		name string
		row  deltacomm.Row
		want int64
	}{
		{
			name: "gift row itemNum",
			row:  deltacomm.Row{"_itemNum": int64(3000)},
			want: 3000,
		},
		{
			name: "bare num",
			row:  deltacomm.Row{"_num": int64(1500)},
			want: 1500,
		},
		{
			name: "prefer itemNum",
			row:  deltacomm.Row{"_itemNum": int64(3000), "_num": int64(1500)},
			want: 3000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := giftGemAmount(tt.row); got != tt.want {
				t.Fatalf("giftGemAmount() = %d, want %d", got, tt.want)
			}
		})
	}
}
