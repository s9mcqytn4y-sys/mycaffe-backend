package service

import (
	"errors"
	"testing"
)

type namedGateway struct {
	GatewayClient
	name string
}

func TestChooseGateway(t *testing.T) {
	tenant, platform := &namedGateway{name: "tenant"}, &namedGateway{name: "platform"}
	tests := []struct {
		name     string
		tenant   GatewayClient
		platform GatewayClient
		required bool
		want     GatewayClient
		wantErr  bool
	}{
		{name: "tenant key wins", tenant: tenant, platform: platform, want: tenant},
		{name: "tenant key wins when required", tenant: tenant, platform: platform, required: true, want: tenant},
		{name: "demo falls back to platform", platform: platform, want: platform},
		{name: "production refuses platform", platform: platform, required: true, wantErr: true},
		{name: "typed nil tenant is no tenant", tenant: (*namedGateway)(nil), platform: platform, required: true, wantErr: true},
		{name: "nothing configured", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chooseGateway(tt.tenant, tt.platform, tt.required)
			if tt.wantErr {
				if !errors.Is(err, ErrNoStoreGateway) {
					t.Fatalf("err = %v, want ErrNoStoreGateway", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %v, err %v", got, err)
			}
		})
	}
}
