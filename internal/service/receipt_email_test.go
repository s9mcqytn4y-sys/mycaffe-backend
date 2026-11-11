package service

import (
	"strings"
	"testing"
	"time"

	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/mailer"
)

func TestReceiptEmailDataCarriesLinesTotalsAndLink(t *testing.T) {
	r := &Receipt{
		OrderNumber: "A-014",
		IssuedAt:    time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC),
		Lines: []ReceiptLine{
			{Name: "Classic Iced Latte", Variant: "Besar", Options: []string{"Oat Milk Barista Blend"}, Quantity: 2, Amount: 76000},
			{Name: "Croissant", Quantity: 1, Amount: 25000},
		},
		Subtotal: 101000, Service: 5050, Tax: 10100, Total: 116150,
		Payments: []ReceiptPayment{{Method: "gopay", Status: "paid"}, {Method: "gopay", Status: "paid"}},
	}
	link := &ReceiptLink{URL: "https://demo.test/receipts/abc", ExpiresAt: time.Date(2027, 2, 9, 0, 0, 0, 0, time.UTC)}

	d := receiptEmailData(r, link)
	if d.PaymentMethod != "GoPay" || d.DiscountLabel != "" || d.ServiceFeeLabel == "" || len(d.Items) != 2 {
		t.Fatalf("unexpected data: %+v", d)
	}
	if d.Items[0].Details != "Besar, Oat Milk Barista Blend" || d.Items[1].Details != "" {
		t.Fatalf("details must list the size then the add-ons: %+v", d.Items)
	}

	d.StoreName = "Kopi Senja"
	out, err := mailer.Render(mailer.TemplateReceipt, d)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Classic Iced Latte", "Oat Milk Barista Blend", rupiah(116150), "GoPay", "https://demo.test/receipts/abc", "status pesanan"} {
		if !strings.Contains(out.HTML, want) {
			t.Fatalf("receipt html is missing %q", want)
		}
	}
	if !strings.Contains(out.Text, "https://demo.test/receipts/abc") {
		t.Fatal("receipt text must carry the link")
	}
}
