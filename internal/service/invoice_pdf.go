package service

import (
	"bytes"
	"fmt"
	"strings"
)

const invoicePDFLinesPerPage = 56

// renderTextPDF writes monospaced lines to an A4 PDF using the built-in Courier font, so invoices need no PDF
// dependency. shortcut: characters outside ASCII print as "?", embed a font if customer names need them.
func renderTextPDF(lines []string) []byte {
	var pages [][]string
	for len(lines) > invoicePDFLinesPerPage {
		pages = append(pages, lines[:invoicePDFLinesPerPage])
		lines = lines[invoicePDFLinesPerPage:]
	}
	pages = append(pages, lines)

	// Objects: 1 catalog, 2 page tree, 3 font, then a page and its content stream per page.
	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 4+i*2)
	}
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Courier >>",
	}
	for i, page := range pages {
		var content strings.Builder
		content.WriteString("BT /F1 10 Tf 13 TL 50 800 Td\n")
		for _, line := range page {
			fmt.Fprintf(&content, "(%s) Tj T*\n", pdfEscape(line))
		}
		content.WriteString("ET")
		objects = append(objects,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", 5+i*2),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", content.Len(), content.String()))
	}

	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, obj := range objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return out.Bytes()
}

func pdfEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\\' || r == '(' || r == ')':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 32 || r > 126:
			b.WriteByte('?')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// invoiceDocumentNumber is the number printed on the invoice and used as its file name.
func invoiceDocumentNumber(number int) string { return fmt.Sprintf("INV-%06d", number) }

// invoicePDF lays the invoice out as text: 78 columns fit an A4 page at 10 pt Courier.
func invoicePDF(storeName string, inv *Invoice) []byte {
	row := func(left, right string) string { return fmt.Sprintf("%-50.50s %27.27s", left, right) }
	lines := []string{storeName, "", "INVOICE " + invoiceDocumentNumber(inv.Number),
		"Tanggal terbit : " + inv.IssuedAt.Format("2006-01-02"), "Jatuh tempo    : " + inv.DueDate, "",
		"Kepada         : " + inv.CustomerName}
	if inv.TaxID != "" {
		lines = append(lines, "NPWP           : "+inv.TaxID)
	}
	lines = append(lines, "", row("Pesanan", "Jumlah"), strings.Repeat("-", 78))
	for _, o := range inv.Orders {
		lines = append(lines, row(o.OrderNumber+"  "+o.CreatedAt.Format("2006-01-02"), rupiah(o.TotalAmount)))
	}
	lines = append(lines, strings.Repeat("-", 78), row("Total", rupiah(inv.TotalAmount)), row("Sudah dibayar", rupiah(inv.PaidAmount)),
		row("Sisa tagihan", rupiah(inv.Remaining)))
	if inv.Note != "" {
		lines = append(lines, "", "Catatan: "+inv.Note)
	}
	return renderTextPDF(lines)
}
