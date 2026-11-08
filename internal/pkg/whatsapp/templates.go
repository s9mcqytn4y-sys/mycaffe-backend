package whatsapp

import (
	"fmt"
	"strings"
)

// TemplateName identifies an approved WhatsApp message template.
type TemplateName string

const (
	// TemplateOrderReceipt: Pesan struk digital resmi kepada pelanggan setelah transaksi selesai.
	TemplateOrderReceipt TemplateName = "cassy_order_receipt"

	// TemplateKitchenTicket: Notifikasi tiket pesanan ronde baru untuk staf dapur/barista.
	TemplateKitchenTicket TemplateName = "cassy_kitchen_ticket"

	// TemplateOrderReady: Notifikasi pesanan telah siap saji di meja pelanggan.
	TemplateOrderReady TemplateName = "cassy_order_ready"

	// TemplateStaffPINReset: Pengiriman token reset PIN kasir/staf gerai.
	TemplateStaffPINReset TemplateName = "cassy_staff_pin_reset"

	// TemplateDailySummary: Ringkasan omset dan penutupan shift kasir kepada pemilik gerai.
	TemplateDailySummary TemplateName = "cassy_daily_summary"
)

// OrderReceiptParams parameterizes TemplateOrderReceipt.
type OrderReceiptParams struct {
	CustomerName string
	OrderNumber  string
	StoreName    string
	TotalRupiah  string
	PaymentType  string
	ReceiptURL   string
}

// Format renders the params into newline-delimited body parameters for Meta WhatsApp Cloud API / Bridge.
func (p OrderReceiptParams) Format() string {
	name := strings.TrimSpace(p.CustomerName)
	if name == "" {
		name = "Pelanggan"
	}
	return fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		name,
		p.OrderNumber,
		p.StoreName,
		p.TotalRupiah,
		p.PaymentType,
		p.ReceiptURL,
	)
}

// OrderReadyParams parameterizes TemplateOrderReady.
type OrderReadyParams struct {
	CustomerName string
	OrderNumber  string
	StoreName    string
	TableNumber  string
}

// Format renders the params into newline-delimited body parameters.
func (p OrderReadyParams) Format() string {
	name := strings.TrimSpace(p.CustomerName)
	if name == "" {
		name = "Pelanggan"
	}
	table := strings.TrimSpace(p.TableNumber)
	if table == "" {
		table = "Kasir"
	}
	return fmt.Sprintf("%s\n%s\n%s\n%s", name, p.OrderNumber, p.StoreName, table)
}

// KitchenTicketParams parameterizes TemplateKitchenTicket.
type KitchenTicketParams struct {
	StationName string
	RoundNumber int
	TableNumber string
	ItemSummary string
}

// Format renders the params into newline-delimited body parameters.
func (p KitchenTicketParams) Format() string {
	return fmt.Sprintf("%s\n%d\n%s\n%s", p.StationName, p.RoundNumber, p.TableNumber, p.ItemSummary)
}
