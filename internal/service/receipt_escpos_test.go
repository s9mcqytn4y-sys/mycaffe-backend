package service_test

import (
	"bytes"
	"flag"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata golden files")

func sampleReceipt() *service.Receipt {
	return &service.Receipt{
		Kind:        service.ReceiptKindSale,
		Store:       service.ReceiptStore{Name: "MyCaffe Demo Store - Central", Address: "Jl. Sudirman No. 101, Jakarta", Phone: "081234567890"},
		OrderNumber: "ORD-20261104-0001", QueueNumber: "A001", TableNumber: "T3", LocalTime: "04/11/2026 09:15",
		StaffName: "Budi", PaymentStat: "paid",
		Lines: []service.ReceiptLine{
			{Name: "Es Kopi Susu Gula Aren Spesial Ukuran Besar", Variant: "Large", Options: []string{"Extra shot", "- 2x Sirup"}, Notes: "Tanpa és", Quantity: 2, UnitPrice: 28000, Amount: 56000},
			{Name: "Croissant", Quantity: 1, UnitPrice: 22000, Amount: 22000},
		},
		Subtotal: 78000, Discount: 5000, Tax: 7300, Service: 3650, Total: 83950, Paid: 83950,
		Payments: []service.ReceiptPayment{{Method: "cash", Amount: 83950, CashTendered: 100000, Change: 16050, Status: "paid"}},
	}
}

// controlSeq strips ESC/POS control sequences and the QR payload, leaving the text a printer shows.
var controlSeq = regexp.MustCompile(`(?s)\x1b@|\x1b[aE].|\x1dV..|\x1d\(k.*?\x1d\(k\x03\x001Q0`)

func TestRenderESCPOS58And80(t *testing.T) {
	for _, width := range []int{58, 80} {
		link := "https://pos.example.test/receipts/abc"
		out, err := service.RenderESCPOS(sampleReceipt(), width, link)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(out, []byte{0x1b, '@'}) || !bytes.HasSuffix(out, []byte{0x1d, 'V', 66, 3}) {
			t.Fatalf("%d mm: must start with ESC @ and end with feed+cut", width)
		}
		if !bytes.Contains(out, append([]byte{0x1d, '(', 'k', byte(len(link) + 3), 0, 49, 80, 48}, link...)) {
			t.Fatalf("%d mm: QR store command with the link is missing", width)
		}
		text := controlSeq.ReplaceAllString(string(out), "")
		cols := map[int]int{58: 32, 80: 48}[width]
		for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			if len(line) > cols {
				t.Fatalf("%d mm: line longer than %d columns: %q", width, cols, line)
			}
			for _, r := range line {
				if r > 126 {
					t.Fatalf("%d mm: non-ASCII byte in %q", width, line)
				}
			}
		}
		golden := map[int]string{58: "testdata/receipt_58mm.golden", 80: "testdata/receipt_80mm.golden"}[width]
		if *updateGolden {
			if err := os.WriteFile(golden, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (run with -update to create it)", err)
		}
		if text != strings.ReplaceAll(string(want), "\r\n", "\n") {
			t.Fatalf("%d mm receipt differs from %s:\n%s", width, golden, text)
		}
	}
	if _, err := service.RenderESCPOS(sampleReceipt(), 76, ""); err == nil {
		t.Fatal("unsupported paper width must fail")
	}
}

func TestRenderESCPOSRefundAndVoid(t *testing.T) {
	r := sampleReceipt()
	r.Kind = service.ReceiptKindRefund
	r.Refund = &service.ReceiptRefund{Method: "cash", Amount: 83950, Reason: "Komplain pelanggan", StockReturned: false}
	out, _ := service.RenderESCPOS(r, 58, "")
	text := controlSeq.ReplaceAllString(string(out), "")
	for _, want := range []string{"*** REFUND ***", "REFUND Tunai", "Rp83.950", "Alasan: Komplain pelanggan", "Stok: dibuang"} {
		if !strings.Contains(text, want) {
			t.Fatalf("refund receipt missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(string(out), "\x1d(k") {
		t.Fatal("no QR without a link")
	}
	r.Kind, r.Refund, r.VoidReason = service.ReceiptKindVoid, nil, "Salah input"
	out, _ = service.RenderESCPOS(r, 80, "")
	text = controlSeq.ReplaceAllString(string(out), "")
	for _, want := range []string{"*** PEMBATALAN ***", "Total dibatalkan", "Alasan: Salah input"} {
		if !strings.Contains(text, want) {
			t.Fatalf("void receipt missing %q:\n%s", want, text)
		}
	}
}
