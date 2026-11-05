package printfmt

import (
	"bytes"
	"strings"
	"testing"
)

func sampleTicket() *Ticket {
	return &Ticket{
		Title: "BAR", QueueNumber: "A012", OrderNumber: "ORD-20261106-0012", LocalTime: "06/11/2026 09:15",
		CustomerName: `Dina "Kopi"`,
		Lines: []Line{
			{Name: "Caffe Latte", Variant: "Large", Quantity: 2,
				Options: []string{"Oat Milk", "- Whipped Oat Cold Foam", "Extra Shot", "Less Ice", "Hazelnut"}, Notes: "tanpa gula"},
			{Name: "Americano", Quantity: 1},
		},
	}
}

func TestStationTicket(t *testing.T) {
	out, err := StationTicket(sampleTicket(), 58, 2, "Terima kasih")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"A012", "BAR", "ORD-20261106-0012", "2x Caffe Latte (Large)", "- Oat Milk", "- Whipped Oat Cold Foam", "Catatan: tanpa gula", "1x Americano", "Terima kasih"} {
		if !strings.Contains(s, want) {
			t.Errorf("ticket misses %q", want)
		}
	}
	if strings.Contains(s, "Rp") || strings.Contains(s, "32000") {
		t.Error("tickets never show prices")
	}
	if n := bytes.Count(out, FeedCut); n != 2 {
		t.Errorf("two copies must cut twice, got %d", n)
	}
	for _, line := range strings.Split(s, "\n") {
		if clean := strings.Map(func(r rune) rune {
			if r < 32 {
				return -1
			}
			return r
		}, line); len(clean) > 32+3 { // control sequences may share a line with text
			t.Errorf("line wider than 58 mm paper: %q", clean)
		}
	}
	if _, err := StationTicket(sampleTicket(), 70, 1, ""); err == nil {
		t.Error("70 mm paper must be rejected")
	}
	tbl := sampleTicket()
	tbl.TableNumber, tbl.RoundNumber, tbl.Reprint = "7", 2, true
	out, _ = StationTicket(tbl, 80, 1, "")
	tbl.Offline = true
	out, _ = StationTicket(tbl, 80, 1, "")
	for _, want := range []string{"MEJA 7", "Ronde", "CETAK ULANG", "Antrean", "OFFLINE"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("dine-in reprint misses %q", want)
		}
	}
}

func TestLabels(t *testing.T) {
	out, err := Labels(sampleTicket(), 40, 30, 1)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if n := strings.Count(s, "PRINT 1,1"); n != 3 {
		t.Fatalf("one label per cup (2 + 1), got %d", n)
	}
	for _, want := range []string{"A012  1/3", "A012  3/3", "Caffe Latte Large", "+ Oat Milk", "+ Whipped Oat Cold"} {
		if !strings.Contains(s, want) {
			t.Errorf("labels miss %q", want)
		}
	}
	if strings.Contains(s, "Hazelnut") {
		t.Error("labels show at most three options")
	}
	if strings.Contains(s, `"Kopi"`) || strings.Contains(s, "Rp") {
		t.Error("quotes must be escaped and prices never printed")
	}
	for _, line := range strings.Split(s, "\r\n") {
		if strings.HasPrefix(line, "TEXT") {
			text := line[strings.Index(line, `1,1,"`)+5 : len(line)-1]
			if len(text) > (40*8-16)/12 {
				t.Errorf("label text wider than 40 mm: %q", text)
			}
		}
	}
	if _, err := Labels(sampleTicket(), 20, 30, 1); err == nil {
		t.Error("20 mm labels must be rejected")
	}
}
