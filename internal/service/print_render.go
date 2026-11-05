package service

import (
	"bytes"
	"fmt"
	"strings"
)

// Station tickets, checker slips (ESC/POS) and cup labels (TSPL) for the print queue (v0.9 PR 9.4,
// W8). No prices on any of them (brand guidelines §5.2). Copies are repeated inside the payload so
// the print agent sends each job exactly once.

// PrintTicket is the header and lines of one ticket, checker slip or label set.
type PrintTicket struct {
	Title        string // BAR, DAPUR, CHECKER
	QueueNumber  string
	TableNumber  string
	OrderNumber  string
	CustomerName string
	LocalTime    string
	RoundNumber  int // 0 when the order has no rounds
	Reprint      bool
	Lines        []ReceiptLine
}

var escDoubleOn, escDoubleOff = []byte{0x1d, '!', 0x11}, []byte{0x1d, '!', 0}

// RenderStationTicket prints a kitchen, bar or checker ticket: queue or table in double size, then
// the lines with their options and notes.
func RenderStationTicket(t *PrintTicket, widthMM, copies int, footer string) ([]byte, error) {
	cols := map[int]int{58: 32, 80: 48}[widthMM]
	if cols == 0 {
		return nil, ErrUnsupportedPaperWidth
	}
	p := &escposWriter{cols: cols}
	for c := 0; c < max(copies, 1); c++ {
		p.raw(escInit)
		p.raw(escAlignMid)
		p.raw(escDoubleOn)
		head := t.QueueNumber
		if t.TableNumber != "" {
			head = "MEJA " + t.TableNumber
		}
		p.line(head)
		p.raw(escDoubleOff)
		p.raw(escBoldOn)
		p.line(t.Title)
		p.raw(escBoldOff)
		if t.Reprint {
			p.line("*** CETAK ULANG ***")
		}
		p.raw(escAlignLeft)
		p.pair("No", t.OrderNumber)
		if t.TableNumber != "" && t.QueueNumber != "" {
			p.pair("Antrean", t.QueueNumber)
		}
		if t.RoundNumber > 0 {
			p.pair("Ronde", fmt.Sprint(t.RoundNumber))
		}
		p.pair("Waktu", t.LocalTime)
		if t.CustomerName != "" {
			p.pair("Tamu", t.CustomerName)
		}
		p.sep()
		for _, l := range t.Lines {
			name := fmt.Sprintf("%dx %s", l.Quantity, l.Name)
			if l.Variant != "" {
				name += " (" + l.Variant + ")"
			}
			p.raw(escBoldOn)
			p.wrap(name, "   ")
			p.raw(escBoldOff)
			for _, o := range l.Options {
				if strings.HasPrefix(o, "- ") {
					p.wrap(o, "      ")
				} else {
					p.wrap("- "+o, "    ")
				}
			}
			if l.Notes != "" {
				p.wrap("Catatan: "+l.Notes, "   ")
			}
		}
		p.sep()
		if footer != "" {
			p.raw(escAlignMid)
			p.wrap(footer, "")
			p.raw(escAlignLeft)
		}
		p.raw(escFeedCut)
	}
	return p.buf.Bytes(), nil
}

// RenderLabels prints one TSPL label per cup: queue number, item, up to three options and the
// guest's name. 203 dpi: 8 dots per mm; font "2" is 12x20 dots.
func RenderLabels(t *PrintTicket, widthMM, heightMM, copies int) ([]byte, error) {
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
				s = strings.ReplaceAll(asciiOnly(s), `"`, "'")
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
