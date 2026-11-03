package service

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// ESC/POS rendering for 58 mm (32 columns) and 80 mm (48 columns) thermal printers, sent as raw bytes
// to a print bridge or tablet. Text is plain ASCII: other characters print as '?'.

var ErrUnsupportedPaperWidth = errors.New("paper width must be 58 or 80 mm")

var (
	escInit      = []byte{0x1b, '@'}
	escAlignLeft = []byte{0x1b, 'a', 0}
	escAlignMid  = []byte{0x1b, 'a', 1}
	escBoldOn    = []byte{0x1b, 'E', 1}
	escBoldOff   = []byte{0x1b, 'E', 0}
	escFeedCut   = []byte{0x1d, 'V', 66, 3} // feed 3 lines, then partial cut
)

var paymentMethodLabels = map[string]string{
	"cash": "Tunai", "qris": "QRIS", "static_qris": "QRIS Statis", "edc": "EDC", "gopay": "GoPay",
	"shopeepay": "ShopeePay", "bank_transfer": "Transfer Bank",
}

// RenderESCPOS prints r for a paper width in mm; qrURL (the digital receipt link) is printed as a QR
// code when not empty.
func RenderESCPOS(r *Receipt, widthMM int, qrURL string) ([]byte, error) {
	cols := map[int]int{58: 32, 80: 48}[widthMM]
	if cols == 0 {
		return nil, ErrUnsupportedPaperWidth
	}
	p := &escposWriter{cols: cols}
	p.raw(escInit)

	p.raw(escAlignMid)
	p.raw(escBoldOn)
	p.wrap(r.Store.Name, "")
	p.raw(escBoldOff)
	p.wrap(r.Store.Address, "")
	p.wrap(r.Store.Phone, "")
	switch r.Kind {
	case ReceiptKindRefund:
		p.line("*** REFUND ***")
	case ReceiptKindVoid:
		p.line("*** PEMBATALAN ***")
	}
	p.raw(escAlignLeft)
	p.sep()
	p.pair("No", r.OrderNumber)
	p.pair("Antrean", r.QueueNumber)
	if r.TableNumber != "" {
		p.pair("Meja", r.TableNumber)
	}
	p.pair("Waktu", r.LocalTime)
	if r.StaffName != "" {
		p.pair("Kasir", r.StaffName)
	}
	p.sep()

	for _, l := range r.Lines {
		name := l.Name
		if l.Variant != "" {
			name += " (" + l.Variant + ")"
		}
		p.wrap(name, "")
		for _, o := range l.Options {
			if strings.HasPrefix(o, "- ") { // nested sub-option
				p.wrap(o, "    ")
			} else {
				p.wrap("+ "+o, "  ")
			}
		}
		if l.Notes != "" {
			p.wrap("* "+l.Notes, "  ")
		}
		p.pair(fmt.Sprintf("  %d x %s", l.Quantity, rupiah(l.UnitPrice)), rupiah(l.Amount))
	}
	p.sep()

	switch r.Kind {
	case ReceiptKindRefund:
		p.raw(escBoldOn)
		p.pair("REFUND "+methodLabel(r.Refund.Method), rupiah(r.Refund.Amount))
		p.raw(escBoldOff)
		p.wrap("Alasan: "+r.Refund.Reason, "")
		stock := "dibuang"
		if r.Refund.StockReturned {
			stock = "dikembalikan"
		}
		p.wrap("Stok: "+stock, "")
	case ReceiptKindVoid:
		p.pair("Total dibatalkan", rupiah(r.Total))
		p.wrap("Alasan: "+r.VoidReason, "")
	default:
		p.pair("Subtotal", rupiah(r.Subtotal))
		if r.Discount > 0 {
			p.pair("Diskon", rupiah(-r.Discount))
		}
		if r.Tax > 0 {
			p.pair("Pajak (PB1)", rupiah(r.Tax))
		}
		if r.Service > 0 {
			p.pair("Service", rupiah(r.Service))
		}
		p.raw(escBoldOn)
		p.pair("TOTAL", rupiah(r.Total))
		p.raw(escBoldOff)
		for _, pay := range r.Payments {
			label := methodLabel(pay.Method)
			if pay.SplitLabel != "" {
				label += " - " + pay.SplitLabel
			}
			p.pair(label, rupiah(pay.Amount))
			if pay.CashTendered > 0 {
				p.pair("  Diterima", rupiah(pay.CashTendered))
				p.pair("  Kembali", rupiah(pay.Change))
			}
		}
		if r.Refunded > 0 {
			p.pair("Sudah direfund", rupiah(r.Refunded))
		}
		if r.PaymentStat == "unpaid" || r.PaymentStat == "partially_paid" {
			p.pair("Sisa tagihan", rupiah(r.Total-r.Paid))
		}
	}

	p.raw(escAlignMid)
	if qrURL != "" {
		p.qr(qrURL)
		p.line("Struk digital (90 hari)")
	}
	p.line("Terima kasih")
	p.raw(escAlignLeft)
	p.raw(escFeedCut)
	return p.buf.Bytes(), nil
}

type escposWriter struct {
	buf  bytes.Buffer
	cols int
}

func (p *escposWriter) raw(b []byte) { p.buf.Write(b) }

func (p *escposWriter) line(s string) {
	p.buf.WriteString(asciiOnly(s))
	p.buf.WriteByte('\n')
}

func (p *escposWriter) sep() { p.line(strings.Repeat("-", p.cols)) }

// pair prints left and right on one line, the right part flush right; a left part that does not fit
// goes on its own line first.
func (p *escposWriter) pair(left, right string) {
	left, right = asciiOnly(left), asciiOnly(right)
	if len(left)+1+len(right) > p.cols {
		p.wrap(left, "")
		left = ""
	}
	p.line(left + strings.Repeat(" ", p.cols-len(left)-len(right)) + right)
}

// wrap breaks s into lines of at most cols characters at spaces, prefixing continuation lines with indent.
func (p *escposWriter) wrap(s, indent string) {
	s = asciiOnly(strings.TrimSpace(s))
	if s == "" {
		return
	}
	cur := indent
	for _, word := range strings.Fields(s) {
		for len(indent)+len(word) > p.cols { // a single word longer than the line is hard-split
			if strings.TrimSpace(cur) != "" {
				p.line(cur)
				cur = indent
			}
			cut := p.cols - len(indent)
			p.line(indent + word[:cut])
			word = word[cut:]
		}
		switch {
		case strings.TrimSpace(cur) == "":
			cur = indent + word
		case len(cur)+1+len(word) <= p.cols:
			cur += " " + word
		default:
			p.line(cur)
			cur = indent + word
		}
	}
	if strings.TrimSpace(cur) != "" {
		p.line(cur)
	}
}

// qr prints a QR code (model 2, module size 6, error correction M) with GS ( k.
func (p *escposWriter) qr(data string) {
	d := []byte(data)
	n := len(d) + 3
	p.raw([]byte{0x1d, '(', 'k', 4, 0, 49, 65, 50, 0})
	p.raw([]byte{0x1d, '(', 'k', 3, 0, 49, 67, 6})
	p.raw([]byte{0x1d, '(', 'k', 3, 0, 49, 69, 49})
	p.raw([]byte{0x1d, '(', 'k', byte(n % 256), byte(n / 256), 49, 80, 48})
	p.raw(d)
	p.raw([]byte{0x1d, '(', 'k', 3, 0, 49, 81, 48})
	p.buf.WriteByte('\n')
}

func asciiOnly(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r > 126 {
			return '?'
		}
		return r
	}, s)
}

func methodLabel(method string) string {
	if l, ok := paymentMethodLabels[method]; ok {
		return l
	}
	return method
}

// rupiah formats integer rupiah with dot thousands separators, e.g. Rp12.500 or -Rp1.000.
func rupiah(v int64) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	digits := fmt.Sprint(v)
	var b strings.Builder
	for i, c := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	return sign + "Rp" + b.String()
}
