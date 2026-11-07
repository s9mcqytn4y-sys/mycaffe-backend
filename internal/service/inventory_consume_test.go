package service

import "testing"

func TestRoundHalfUpDiv(t *testing.T) {
	for _, c := range []struct{ a, b, want int64 }{{5400000, 1000, 5400}, {1499, 1000, 1}, {1500, 1000, 2}, {-1500, 1000, -2}, {0, 1000, 0}} {
		if got := roundHalfUpDiv(c.a, c.b); got != c.want {
			t.Errorf("roundHalfUpDiv(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
