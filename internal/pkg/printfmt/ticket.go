package printfmt

import (
	"bytes"
	"fmt"
	"strings"
)

// Station tickets, checker slips (ESC/POS) and cup labels (TSPL), decision W8. No prices on any of
// them (brand guidelines §5.2). Copies are repeated inside the payload so a job is sent exactly once.

// Line is one order line as printed in the kitchen: no prices. Options starting with "- " are
// nested sub-options.
type Line struct {
	Name     string   `json:"name"`
	Variant  string   `json:"variant,omitempty"`
	Options  []string `json:"options,omitempty"`
	Notes    string   `json:"notes,omitempty"`
	Quantity int      `json:"quantity"`
	// Station is the prep station of the line (bar, kitchen, none): it routes the line to printers.
	Station string `json:"station,omitempty"`
}

// Ticket is the header and lines of one station ticket, checker slip or label set.
type Ticket struct {
	Title        string `json:"title"` // BAR, DAPUR, CHECKER
	QueueNumber  string `json:"queue_number"`
	TableNumber  string `json:"table_number,omitempty"`
	OrderNumber  string `json:"order_number"`
	CustomerName string `json:"customer_name,omitempty"`
	LocalTime    string `json:"local_time"`
	RoundNumber  int    `json:"round_number,omitempty"`
	Reprint      bool   `json:"reprint,omitempty"`
	// Offline marks tickets printed by the LAN agent during an outage (W18): the official number
	// follows on sync.
	Offline bool   `json:"offline,omitempty"`
	Lines   []Line `json:"lines"`
}

// StationTicket prints a kitchen, bar or checker ticket: queue or table in double size, then the
// lines with their options and notes.
func StationTicket(t *Ticket, widthMM, copies int, footer string) ([]byte, error) {
	cols := Columns(widthMM)
	if cols == 0 {
		return nil, ErrUnsupportedPaperWidth
	}
	p := NewWriter(cols)
	for c := 0; c < max(copies, 1); c++ {
		p.Raw(Init)
		p.Raw(AlignMid)
		p.Raw(DoubleOn)
		head := t.QueueNumber
		if t.TableNumber != "" {
			head = "MEJA " + t.TableNumber
		}
		p.Line(head)
		p.Raw(DoubleOff)
		p.Raw(BoldOn)
		p.Line(t.Title)
		p.Raw(BoldOff)
		if t.Offline {
			p.Line("*** OFFLINE - nomor resmi menyusul ***")
		}
		if t.Reprint {
			p.Line("*** CETAK ULANG ***")
		}
		p.Raw(AlignLeft)
		if t.OrderNumber != "" {
			p.Pair("No", t.OrderNumber)
		}
		if t.TableNumber != "" && t.QueueNumber != "" {
			p.Pair("Antrean", t.QueueNumber)
		}
		if t.RoundNumber > 0 {
			p.Pair("Ronde", fmt.Sprint(t.RoundNumber))
		}
		p.Pair("Waktu", t.LocalTime)
		if t.CustomerName != "" {
			p.Pair("Tamu", t.CustomerName)
		}
		p.Sep()
		for _, l := range t.Lines {
			name := fmt.Sprintf("%dx %s", l.Quantity, l.Name)
			if l.Variant != "" {
				name += " (" + l.Variant + ")"
			}
			p.Raw(BoldOn)
			p.Wrap(name, "   ")
			p.Raw(BoldOff)
			for _, o := range l.Options {
				if strings.HasPrefix(o, "- ") {
					p.Wrap(o, "      ")
				} else {
					p.Wrap("- "+o, "    ")
				}
			}
			if l.Notes != "" {
				p.Wrap("Catatan: "+l.Notes, "   ")
			}
		}
		p.Sep()
		if footer != "" {
			p.Raw(AlignMid)
			p.Wrap(footer, "")
			p.Raw(AlignLeft)
		}
		p.Raw(FeedCut)
	}
	return p.Bytes(), nil
}

// Labels prints one TSPL label per cup: queue number, item, up to three options and the guest's
// name. 203 dpi: 8 dots per mm; font "2" is 12x20 dots.
func Labels(t *Ticket, widthMM, heightMM, copies int) ([]byte, error) {
	if widthMM < 25 || widthMM > 110 || heightMM < 15 || heightMM > 100 {
		return nil, fmt.Errorf("label size %dx%d mm is out of range", widthMM, heightMM)
	}
	cols := (widthMM*8 - 16) / 12
	rows := (heightMM*8 - 16) / 24
	var b bytes.Buffer
	total := 0
	for _, l := range t.Lines {
		total += l.Quantity
	}
	n := 0
	for _, l := range t.Lines {
		for unit := 0; unit < l.Quantity; unit++ {
			n++
			text := []string{fmt.Sprintf("%s  %d/%d", t.QueueNumber, n, total)}
			name := l.Name
			if l.Variant != "" {
				name += " " + l.Variant
			}
			text = append(text, name)
			for i, o := range l.Options {
				if i == 3 {
					break
				}
				text = append(text, "+ "+strings.TrimPrefix(o, "- "))
			}
			if t.CustomerName != "" {
				text = append(text, t.CustomerName)
			}
			fmt.Fprintf(&b, "SIZE %d mm,%d mm\r\nGAP 2 mm,0 mm\r\nDIRECTION 1\r\nCLS\r\n", widthMM, heightMM)
			for i, s := range text {
				if i == rows {
					break
				}
				s = strings.ReplaceAll(ASCII(s), `"`, "'")
				if len(s) > cols {
					s = s[:cols]
				}
				fmt.Fprintf(&b, "TEXT 8,%d,\"2\",0,1,1,\"%s\"\r\n", 8+i*24, s)
			}
			fmt.Fprintf(&b, "PRINT 1,%d\r\n", max(copies, 1))
		}
	}
	if b.Len() == 0 {
		return nil, fmt.Errorf("no label lines")
	}
	return b.Bytes(), nil
}
