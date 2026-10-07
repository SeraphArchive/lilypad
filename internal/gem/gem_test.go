package gem

import (
	"errors"
	"testing"
)

func TestApplyFreeFirst(t *testing.T) {
	cases := []struct {
		name string
		b    Balance
		n    int64
		want Balance
		err  error
	}{
		{"free only", Balance{Free: 100, Paid: 50}, 60, Balance{Free: 40, Paid: 50}, nil},
		{"exact free", Balance{Free: 100, Paid: 50}, 100, Balance{Free: 0, Paid: 50}, nil},
		{"spill into paid", Balance{Free: 100, Paid: 50}, 130, Balance{Free: 0, Paid: 20}, nil},
		{"exact total", Balance{Free: 100, Paid: 50}, 150, Balance{Free: 0, Paid: 0}, nil},
		{"insufficient unchanged", Balance{Free: 100, Paid: 50}, 151, Balance{Free: 100, Paid: 50}, ErrInsufficient},
		{"zero noop", Balance{Free: 100, Paid: 50}, 0, Balance{Free: 100, Paid: 50}, nil},
		{"negative noop", Balance{Free: 100, Paid: 50}, -5, Balance{Free: 100, Paid: 50}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Apply(c.b, c.n)
			if !errors.Is(err, c.err) {
				t.Fatalf("err: got %v want %v", err, c.err)
			}
			if got != c.want {
				t.Fatalf("balance: got %+v want %+v", got, c.want)
			}
		})
	}
}

func TestTotal(t *testing.T) {
	if (Balance{Free: 3, Paid: 4}).Total() != 7 {
		t.Fatal("Total wrong")
	}
}
