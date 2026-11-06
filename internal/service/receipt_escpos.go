package service

import (
	"fmt"
	"strings"

	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/printfmt"
)

// ESC/POS rendering for 58 mm (32 columns) and 80 mm (48 columns) thermal printers, sent as raw bytes
// to a print bridge or tablet. Text is plain ASCII: other characters print as '?'.

var ErrUnsupportedPaperWidth = printfmt.ErrUnsupportedPaperWidth

var paymentMethodLabels = map[string]string{
	"cash": "Tunai", "qris": "QRIS", "static_qris": "QRIS Statis", "edc": "EDC", "gopay": "GoPay",
	"shopeepay": "ShopeePay", "bank_transfer": "Transfer Bank",
}

// RenderESCPOS prints r for a paper width in mm; qrURL (the digital receipt link) is printed as a QR
// code when not empty.
func RenderESCPOS(r *Receipt, widthMM int, qrURL string) ([]byte, error) {
	cols := printfmt.Columns(widthMM)
	if cols == 0 {
		return nil, ErrUnsupportedPaperWidth
	}
	p := printfmt.NewWriter(cols)
	p.Raw(printfmt.Init)

	p.Raw(printfmt.AlignMid)
	p.Raw(printfmt.BoldOn)
	p.Wrap(r.Store.Name, "")
	p.Raw(printfmt.BoldOff)
	p.Wrap(r.Store.Address, "")
	p.Wrap(r.Store.Phone, "")
	switch r.Kind {
	case ReceiptKindRefund:
		p.Line("*** REFUND ***")
	case ReceiptKindVoid:
		p.Line("*** PEMBATALAN ***")
	}
	p.Raw(printfmt.AlignLeft)
	p.Sep()
	p.Pair("No", r.OrderNumber)
	p.Pair("Antrean", r.QueueNumber)
	if r.TableNumber != "" {
		p.Pair("Meja", r.TableNumber)
	}
	p.Pair("Waktu", r.LocalTime)
	if r.StaffName != "" {
		p.Pair("Kasir", r.StaffName)
	}
	p.Sep()

	for _, l := range r.Lines {
		name := l.Name
		if l.Variant != "" {
			name += " (" + l.Variant + ")"
		}
		p.Wrap(name, "")
		for _, o := range l.Options {
			if strings.HasPrefix(o, "- ") { // nested sub-option
				p.Wrap(o, "    ")
			} else {
				p.Wrap("+ "+o, "  ")
			}
		}
		if l.Notes != "" {
			p.Wrap("* "+l.Notes, "  ")
		}
		p.Pair(fmt.Sprintf("  %d x %s", l.Quantity, rupiah(l.UnitPrice)), rupiah(l.Amount))
	}
	p.Sep()

	switch r.Kind {
	case ReceiptKindRefund:
		p.Raw(printfmt.BoldOn)
		p.Pair("REFUND "+methodLabel(r.Refund.Method), rupiah(r.Refund.Amount))
		p.Raw(printfmt.BoldOff)
		p.Wrap("Alasan: "+r.Refund.Reason, "")
		stock := "dibuang"
		if r.Refund.StockReturned {
			stock = "dikembalikan"
		}
		p.Wrap("Stok: "+stock, "")
	case ReceiptKindVoid:
		p.Pair("Total dibatalkan", rupiah(r.Total))
		p.Wrap("Alasan: "+r.VoidReason, "")
	default:
		p.Pair("Subtotal", rupiah(r.Subtotal))
		if r.Discount > 0 {
			p.Pair("Diskon", rupiah(-r.Discount))
		}
		if r.Tax > 0 {
			p.Pair("Pajak (PB1)", rupiah(r.Tax))
		}
		if r.Service > 0 {
			p.Pair("Service", rupiah(r.Service))
		}
		p.Raw(printfmt.BoldOn)
		p.Pair("TOTAL", rupiah(r.Total))
		p.Raw(printfmt.BoldOff)
		for _, pay := range r.Payments {
			label := methodLabel(pay.Method)
			if pay.SplitLabel != "" {
				label += " - " + pay.SplitLabel
			}
			p.Pair(label, rupiah(pay.Amount))
			if pay.Rounding != 0 {
				p.Pair("  Pembulatan", rupiah(pay.Rounding))
			}
			if pay.CashTendered > 0 {
				p.Pair("  Diterima", rupiah(pay.CashTendered))
				p.Pair("  Kembali", rupiah(pay.Change))
			}
		}
		if r.Refunded > 0 {
			p.Pair("Sudah direfund", rupiah(r.Refunded))
		}
		if r.PaymentStat == "unpaid" || r.PaymentStat == "partially_paid" {
			p.Pair("Sisa tagihan", rupiah(r.Total-r.Paid))
		}
	}

	p.Raw(printfmt.AlignMid)
	if qrURL != "" {
		p.QR(qrURL)
		p.Line("Struk digital (90 hari)")
	}
	p.Line("Terima kasih")
	p.Raw(printfmt.AlignLeft)
	p.Raw(printfmt.FeedCut)
	return p.Bytes(), nil
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
