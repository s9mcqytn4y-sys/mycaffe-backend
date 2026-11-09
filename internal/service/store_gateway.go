package service

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
)

// ErrNoStoreGateway means the store has no Midtrans key and may not use the platform account.
var ErrNoStoreGateway = errors.New("store has no Midtrans credentials")

// chooseGateway returns the Midtrans account that owns a store's transactions (ADR-009).
func chooseGateway(tenant, platform GatewayClient, tenantRequired bool) (GatewayClient, error) {
	if client := normalizeGatewayClient(tenant); client != nil {
		return client, nil
	}
	if client := normalizeGatewayClient(platform); client != nil && !tenantRequired {
		return client, nil
	}
	return nil, ErrNoStoreGateway
}

func resolveStoreGateway(ctx context.Context, db *sqlx.DB, storeID uuid.UUID, resolver StoreGatewayResolver,
	platform GatewayClient, tenantRequired bool) (GatewayClient, error) {
	var tenant GatewayClient
	if resolver != nil {
		err := database.WithTenantTx(ctx, db, storeID, func(tx *sqlx.Tx) error {
			var resolveErr error
			tenant, resolveErr = resolver(ctx, tx, storeID)
			return resolveErr
		})
		if err != nil {
			return nil, err
		}
	}
	return chooseGateway(tenant, platform, tenantRequired)
}

func (s *PaymentService) gatewayForStore(ctx context.Context, storeID uuid.UUID) (GatewayClient, error) {
	return resolveStoreGateway(ctx, s.db, storeID, s.storeGatewayResolver, s.midtransClient, s.tenantGatewayRequired)
}

// SetGatewayResolver sends refunds through each store's own Midtrans account.
func (s *OperationalService) SetGatewayResolver(resolver StoreGatewayResolver, tenantRequired bool) {
	s.gatewayResolver = resolver
	s.tenantGatewayRequired = tenantRequired
}

func (s *OperationalService) gatewayForStore(ctx context.Context, storeID uuid.UUID) (GatewayClient, error) {
	return resolveStoreGateway(ctx, s.db, storeID, s.gatewayResolver, s.gateway, s.tenantGatewayRequired)
}
