// Package rbac is the single permission policy of ADR-003 D7: four fixed roles and a static
// role → permission map. Handlers require a permission through route middleware; services that
// perform critical operations re-check it against the trusted actor.
package rbac

import (
	"slices"
	"sort"

	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
)

type Permission string

const (
	StoreSettingsManage Permission = "store.settings.manage"
	StaffManage         Permission = "staff.manage"
	CatalogManage       Permission = "catalog.manage"
	CatalogCostRead     Permission = "catalog.cost.read"
	ReportFinanceRead   Permission = "report.finance.read"

	OrderPOSCreate         Permission = "order.pos.create"
	OrderReadStore         Permission = "order.read.store"
	PaymentTenderDirect    Permission = "payment.tender.direct"
	PaymentInitiateGateway Permission = "payment.initiate.gateway"
	TabManage              Permission = "tab.manage"
	SplitBillManage        Permission = "split_bill.manage"
	ShiftOpen              Permission = "shift.open"
	ShiftClose             Permission = "shift.close"
	OrderVoid              Permission = "order.void"
	PaymentRefund          Permission = "payment.refund"
	OrderDiscountApply     Permission = "order.discount.apply"
	StockOverride          Permission = "stock.override"

	KDSBaristaRead   Permission = "kds.ticket.read.barista"
	KDSBaristaUpdate Permission = "kds.ticket.update.barista"
	KDSKitchenRead   Permission = "kds.ticket.read.kitchen"
	KDSKitchenUpdate Permission = "kds.ticket.update.kitchen"

	AvailabilityToggleBeverage Permission = "item.availability.toggle.beverage"
	AvailabilityToggleFood     Permission = "item.availability.toggle.food"

	TableManage   Permission = "table.manage"
	TableQRManage Permission = "table.qr.manage"
)

// Options are per-store settings that widen a role (owner decision C3).
type Options struct {
	// CashierHandlesBar gives the cashier role the barista KDS and beverage availability powers.
	CashierHandlesBar bool
}

var all = []Permission{
	StoreSettingsManage, StaffManage, CatalogManage, CatalogCostRead, ReportFinanceRead,
	OrderPOSCreate, OrderReadStore, PaymentTenderDirect, PaymentInitiateGateway, TabManage, SplitBillManage,
	ShiftOpen, ShiftClose, OrderVoid, PaymentRefund, OrderDiscountApply, StockOverride,
	KDSBaristaRead, KDSBaristaUpdate, KDSKitchenRead, KDSKitchenUpdate,
	AvailabilityToggleBeverage, AvailabilityToggleFood, TableManage, TableQRManage,
}

var cashierBase = []Permission{
	OrderPOSCreate, OrderReadStore, PaymentTenderDirect, PaymentInitiateGateway, TabManage, SplitBillManage,
	ShiftOpen, ShiftClose,
	// Void/refund/discount/stock override are granted, but the service applies the owner-PIN
	// thresholds of O1/O4 (stock override always needs the owner PIN).
	OrderVoid, PaymentRefund, OrderDiscountApply, StockOverride,
	KDSBaristaRead, KDSKitchenRead,
}

var roleMatrix = map[domain.UserRole][]Permission{
	domain.RoleOwner:   all,
	domain.RoleCashier: cashierBase,
	domain.RoleBarista: {KDSBaristaRead, KDSBaristaUpdate, AvailabilityToggleBeverage},
	domain.RoleKitchen: {KDSKitchenRead, KDSKitchenUpdate, AvailabilityToggleFood},
}

// Permissions returns the effective, sorted permission set of a role. Unknown roles get none.
func Permissions(role domain.UserRole, opts Options) []Permission {
	perms := slices.Clone(roleMatrix[role])
	if role == domain.RoleCashier && opts.CashierHandlesBar {
		perms = append(perms, KDSBaristaUpdate, AvailabilityToggleBeverage)
	}
	sort.Slice(perms, func(i, j int) bool { return perms[i] < perms[j] })
	return slices.Compact(perms)
}

func Has(role domain.UserRole, opts Options, p Permission) bool {
	return slices.Contains(Permissions(role, opts), p)
}

// Strings converts permissions for API responses.
func Strings(perms []Permission) []string {
	out := make([]string, len(perms))
	for i, p := range perms {
		out[i] = string(p)
	}
	return out
}
