package printfmt

import "testing"

func TestSelectRoutesByStation(t *testing.T) {
	lines := []Line{{Name: "Kopi", Station: "bar"}, {Name: "Roti", Station: "kitchen"}, {Name: "Air", Station: "none"}}
	cases := map[string]struct {
		kind string
		n    int
	}{"bar": {"ticket", 1}, "kitchen": {"ticket", 1}, "checker": {"checker", 3}, "label": {"label", 1}, "unknown": {"", 0}}
	for station, want := range cases {
		kind, sel := Select(station, lines)
		if kind != want.kind || len(sel) != want.n {
			t.Errorf("%s: got %s/%d, want %s/%d", station, kind, len(sel), want.kind, want.n)
		}
	}
	if _, sel := Select("label", lines); sel[0].Name != "Kopi" {
		t.Error("labels print the bar lines")
	}
}
