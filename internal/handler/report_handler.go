package handler

import (
	"encoding/csv"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

// ReportHandler serves the B2 owner reports as JSON or CSV.
type ReportHandler struct {
	svc *service.ReportService
}

func NewReportHandler(svc *service.ReportService) *ReportHandler {
	return &ReportHandler{svc: svc}
}

var salesCSVKeyHeader = map[string]string{
	service.ReportGroupDay: "tanggal", service.ReportGroupHour: "jam", service.ReportGroupMethod: "metode",
	service.ReportGroupCategory: "kategori", service.ReportGroupItem: "item",
}

// Sales handles GET /api/v1/reports/sales?from=&to=&group=day|hour|method|category|item&format=json|csv.
func (h *ReportHandler) Sales(c *gin.Context) {
	format := c.DefaultQuery("format", "json")
	if format != "json" && format != "csv" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "VALIDATION_FAILED", "error": "Periksa kembali isian yang ditandai.", "fields": map[string]string{"format": "harus json atau csv"}})
		return
	}
	group := c.DefaultQuery("group", service.ReportGroupDay)
	report, err := h.svc.Sales(c.Request.Context(), currentStaff(c).Staff.Store.ID, c.Query("from"), c.Query("to"), group)
	if err != nil {
		writeReportError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	if format == "json" {
		c.JSON(http.StatusOK, report)
		return
	}
	records := [][]string{{salesCSVKeyHeader[group], "jumlah_order", "jumlah_pembayaran", "kuantitas", "penjualan_kotor", "diskon", "pajak", "service", "total", "refund", "bersih"}}
	for _, r := range report.Rows {
		records = append(records, []string{r.Key, itoa(r.Orders), itoa(r.Payments), itoa(r.Quantity), itoa(r.GrossSales), itoa(r.Discount),
			itoa(r.Tax), itoa(r.ServiceCharge), itoa(r.Total), itoa(r.Refunded), itoa(r.Net)})
	}
	writeCSV(c, fmt.Sprintf("penjualan-%s-%s-%s.csv", group, report.From, report.To), records)
}

// Summary handles GET /api/v1/reports/summary?from=&to=&format=json|csv.
func (h *ReportHandler) Summary(c *gin.Context) {
	format := c.DefaultQuery("format", "json")
	if format != "json" && format != "csv" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "VALIDATION_FAILED", "error": "Periksa kembali isian yang ditandai.", "fields": map[string]string{"format": "harus json atau csv"}})
		return
	}
	sum, err := h.svc.Summary(c.Request.Context(), currentStaff(c).Staff.Store.ID, c.Query("from"), c.Query("to"))
	if err != nil {
		writeReportError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	if format == "json" {
		c.JSON(http.StatusOK, sum)
		return
	}
	records := [][]string{{"komponen", "nilai"},
		{"jumlah_order", itoa(sum.Orders)}, {"penjualan_kotor", itoa(sum.GrossSales)}, {"diskon", itoa(sum.Discount)},
		{"pajak", itoa(sum.Tax)}, {"service", itoa(sum.ServiceCharge)}, {"total", itoa(sum.Total)},
		{"refund", itoa(sum.Refunded)}, {"bersih", itoa(sum.Net)}, {"jumlah_void", itoa(sum.VoidCount)},
		{"nilai_void", itoa(sum.VoidAmount)}, {"kas_penjualan", itoa(sum.CashSales)}, {"kas_refund", itoa(sum.CashRefunds)},
		{"kas_bersih", itoa(sum.CashNet)}, {"kas_pembulatan", itoa(sum.CashRounding)}, {"pengeluaran_kas_outlet", itoa(sum.OutletExpenses)}}
	for _, m := range sum.RefundsBy {
		records = append(records, []string{"refund_" + m.Method, itoa(m.Amount)})
	}
	writeCSV(c, fmt.Sprintf("ringkasan-%s-%s.csv", sum.From, sum.To), records)
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func writeCSV(c *gin.Context, filename string, records [][]string) {
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Status(http.StatusOK)
	w := csv.NewWriter(c.Writer)
	if err := w.WriteAll(records); err != nil {
		log.Printf("[API] request_id=%s csv write error: %v", c.GetString(RequestIDHeader), err)
	}
}

func writeReportError(c *gin.Context, err error) {
	var validation *service.ValidationError
	if errors.As(err, &validation) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "VALIDATION_FAILED", "error": "Periksa kembali isian yang ditandai.", "fields": validation.Fields})
		return
	}
	log.Printf("[API] request_id=%s %s %s error: %v", c.GetString(RequestIDHeader), c.Request.Method, c.FullPath(), err)
	authError(c, http.StatusServiceUnavailable, "SERVICE_TEMPORARILY_UNAVAILABLE", "Layanan sedang bermasalah. Coba lagi.")
}

// GrossProfit handles GET /api/v1/reports/gross-profit?from=&to=&format=json|csv (COGS per menu item, X10).
func (h *ReportHandler) GrossProfit(c *gin.Context) {
	format := c.DefaultQuery("format", "json")
	if format != "json" && format != "csv" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "VALIDATION_FAILED", "error": "Periksa kembali isian yang ditandai.", "fields": map[string]string{"format": "harus json atau csv"}})
		return
	}
	report, err := h.svc.GrossProfit(c.Request.Context(), currentStaff(c).Staff.Store.ID, c.Query("from"), c.Query("to"))
	if err != nil {
		writeReportError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	if format == "json" {
		c.JSON(http.StatusOK, report)
		return
	}
	records := [][]string{{"menu", "kuantitas", "pendapatan", "hpp", "laba_kotor", "margin_bps", "baris_tanpa_hpp", "pendapatan_tanpa_hpp"}}
	for _, r := range report.Rows {
		records = append(records, []string{r.Key, itoa(r.Quantity), itoa(r.Revenue), itoa(r.COGS), itoa(r.GrossProfit), itoa(r.MarginBPS),
			itoa(r.LinesNoCOGS), itoa(r.RevenueNoCOGS)})
	}
	writeCSV(c, fmt.Sprintf("laba-kotor-%s-%s.csv", report.From, report.To), records)
}

// Channels handles GET /api/v1/reports/channels?from=&to=&format=json|csv (X14).
func (h *ReportHandler) Channels(c *gin.Context) {
	format := c.DefaultQuery("format", "json")
	if format != "json" && format != "csv" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "VALIDATION_FAILED", "error": "Periksa kembali isian yang ditandai.", "fields": map[string]string{"format": "harus json atau csv"}})
		return
	}
	report, err := h.svc.Channels(c.Request.Context(), currentStaff(c).Staff.Store.ID, c.Query("from"), c.Query("to"))
	if err != nil {
		writeReportError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	if format == "json" {
		c.JSON(http.StatusOK, report)
		return
	}
	records := [][]string{{"kanal", "jumlah_order", "kotor", "refund", "komisi", "bersih"}}
	for _, r := range report.Rows {
		records = append(records, []string{r.Channel, itoa(r.Orders), itoa(r.Gross), itoa(r.Refunded), itoa(r.Commission), itoa(r.Net)})
	}
	writeCSV(c, fmt.Sprintf("kanal-%s-%s.csv", report.From, report.To), records)
}
