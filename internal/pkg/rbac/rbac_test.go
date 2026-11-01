package rbac_test

import (
	"slices"
	"testing"

	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/auth"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
)

// The role enum, JWT role validation and the RBAC matrix must stay in lock-step (ADR-003 D7).
func TestRolesInLockStep(t *testing.T) {
	for _, r := range auth.ValidRoles {
		if !slices.Contains(domain.ValidUserRoles(), domain.UserRole(r)) {
			t.Errorf("JWT role %q is not a domain role", r)
		}
		if len(rbac.Permissions(domain.UserRole(r), rbac.Options{})) == 0 {
			t.Errorf("role %q has no permissions", r)
		}
	}
	if len(auth.ValidRoles) != len(domain.ValidUserRoles()) {
		t.Fatalf("JWT roles %v and domain roles %v differ", auth.ValidRoles, domain.ValidUserRoles())
	}
	if got := rbac.Permissions("manager", rbac.Options{}); len(got) != 0 {
		t.Fatalf("unknown role must have no permissions, got %v", got)
	}
}

func TestMatrix(t *testing.T) {
	cases := []struct {
		role domain.UserRole
		opts rbac.Options
		perm rbac.Permission
		want bool
	}{
		{domain.RoleOwner, rbac.Options{}, rbac.StaffManage, true},
		{domain.RoleCashier, rbac.Options{}, rbac.StaffManage, false},
		{domain.RoleCashier, rbac.Options{}, rbac.OrderPOSCreate, true},
		{domain.RoleCashier, rbac.Options{}, rbac.PaymentTenderDirect, true},
		{domain.RoleCashier, rbac.Options{}, rbac.ReportFinanceRead, false},
		{domain.RoleCashier, rbac.Options{}, rbac.KDSBaristaRead, true},
		{domain.RoleCashier, rbac.Options{}, rbac.KDSBaristaUpdate, false},
		{domain.RoleCashier, rbac.Options{CashierHandlesBar: true}, rbac.KDSBaristaUpdate, true},
		{domain.RoleCashier, rbac.Options{CashierHandlesBar: true}, rbac.KDSKitchenUpdate, false},
		{domain.RoleBarista, rbac.Options{}, rbac.OrderPOSCreate, false},
		{domain.RoleBarista, rbac.Options{}, rbac.KDSBaristaUpdate, true},
		{domain.RoleBarista, rbac.Options{CashierHandlesBar: true}, rbac.PaymentTenderDirect, false},
		{domain.RoleKitchen, rbac.Options{}, rbac.KDSKitchenUpdate, true},
		{domain.RoleKitchen, rbac.Options{}, rbac.KDSBaristaRead, false},
	}
	for _, tc := range cases {
		if got := rbac.Has(tc.role, tc.opts, tc.perm); got != tc.want {
			t.Errorf("Has(%s, %+v, %s) = %v, want %v", tc.role, tc.opts, tc.perm, got, tc.want)
		}
	}
}
