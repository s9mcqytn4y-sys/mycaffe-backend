package service

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestInvoicePDFIsWellFormedAndPaginates(t *testing.T) {
	inv := &Invoice{Number: 7, CustomerName: "PT (Maju) Kopi", DueDate: "2026-12-01", IssuedAt: time.Now(),
		TotalAmount: 1000, Remaining: 1000}
	for i := 0; i < 120; i++ {
		inv.Orders = append(inv.Orders, InvoiceOrder{OrderNumber: fmt.Sprintf("A-%d", i), TotalAmount: 1000, CreatedAt: time.Now()})
	}
	pdf := invoicePDF("Toko Café", inv)

	if !bytes.HasPrefix(pdf, []byte("%PDF-1.4\n")) || !bytes.HasSuffix(pdf, []byte("%%EOF\n")) {
		t.Fatal("missing PDF header or trailer")
	}
	if !bytes.Contains(pdf, []byte(`PT \(Maju\) Kopi`)) || !bytes.Contains(pdf, []byte("Toko Caf?")) {
		t.Fatal("text must be escaped and non-ASCII replaced")
	}
	if !bytes.Contains(pdf, []byte("/Count 3")) {
		t.Fatal("about 130 lines need three pages")
	}
	tail := strings.Split(strings.TrimSpace(string(pdf)), "\n")
	offset, err := strconv.Atoi(tail[len(tail)-2])
	if err != nil || !bytes.HasPrefix(pdf[offset:], []byte("xref\n")) {
		t.Fatalf("startxref must point at the xref table: %v", err)
	}
	for _, m := range []string{"1 0 obj", "3 0 obj"} {
		i := bytes.Index(pdf, []byte(m))
		entry := fmt.Sprintf("%010d 00000 n", i)
		if !bytes.Contains(pdf, []byte(entry)) {
			t.Fatalf("xref has no entry %q for %s", entry, m)
		}
	}
}
