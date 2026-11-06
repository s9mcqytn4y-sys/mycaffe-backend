package printfmt

// Printer is what a printer prints and on what paper (decision W8). The server stores it; the print agent
// caches it so it can print on the LAN during an outage (W18).
type Printer struct {
	Station       string `json:"station"` // bar, kitchen, checker, label
	Format        string `json:"format"`  // escpos, tspl
	PaperWidthMM  int    `json:"paper_width_mm"`
	LabelHeightMM *int   `json:"label_height_mm,omitempty"`
	Copies        int    `json:"copies"`
	Footer        string `json:"footer"`
}

// StationTitles head the station tickets.
var StationTitles = map[string]string{"bar": "BAR", "kitchen": "DAPUR", "checker": "CHECKER"}

// Select returns the print kind (ticket, checker, label) and the lines a printer of this station prints:
// bar and kitchen their own lines, checker every line, label one label per bar line.
func Select(station string, lines []Line) (string, []Line) {
	pick := func(s string) []Line {
		var out []Line
		for _, l := range lines {
			if l.Station == s {
				out = append(out, l)
			}
		}
		return out
	}
	switch station {
	case "bar", "kitchen":
		return "ticket", pick(station)
	case "checker":
		return "checker", lines
	case "label":
		return "label", pick("bar")
	}
	return "", nil
}

// Render prints the ticket for a printer: TSPL labels or an ESC/POS station ticket with the station title.
func Render(p Printer, t Ticket) ([]byte, error) {
	t.Title = StationTitles[p.Station]
	if p.Format == "tspl" {
		h := 30
		if p.LabelHeightMM != nil {
			h = *p.LabelHeightMM
		}
		return Labels(&t, p.PaperWidthMM, h, p.Copies)
	}
	return StationTicket(&t, p.PaperWidthMM, p.Copies, p.Footer)
}
