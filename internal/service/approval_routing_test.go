package service

import (
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/realtime"
)

// ADR-007 RA5: a new approval request reaches only the owner channel; the decision also reaches the
// POS channel where the cashier waits. Order events never reach the owner channel.
func TestApprovalEventRouting(t *testing.T) {
	for event, want := range map[string][]string{
		"approval.requested": {realtime.ChannelOwner},
		"approval.decided":   {realtime.ChannelPOS, realtime.ChannelOwner},
		"order.created":      {realtime.ChannelPOS},
	} {
		msgs := outboxRow{StoreID: uuid.New(), EventType: event}.messages()
		if len(msgs) != 1 || !slices.Equal(msgs[0].Channels, want) {
			t.Errorf("%s: channels %v, want %v", event, msgs[0].Channels, want)
		}
	}
}

// Only staff who may decide approvals get the owner channel on their WebSocket ticket.
func TestOwnerChannelGrant(t *testing.T) {
	owner := &AuthenticatedStaff{Permissions: rbac.Permissions(domain.RoleOwner, rbac.Options{})}
	cashier := &AuthenticatedStaff{Permissions: rbac.Permissions(domain.RoleCashier, rbac.Options{})}
	if !slices.Contains(staffChannels(owner), realtime.ChannelOwner) {
		t.Error("owner must get the owner channel")
	}
	if slices.Contains(staffChannels(cashier), realtime.ChannelOwner) {
		t.Error("cashier must not get the owner channel")
	}
}
