package config

import (
	"strings"
	"testing"
)

// The whatsmeow bridge must never run in production (decision W12), whatever else is configured.
func TestValidateWhatsAppConfigRefusesBridgeInProduction(t *testing.T) {
	cfg := &Config{AppEnv: "production", WhatsAppDriver: "bridge", WhatsAppBridgeURL: "http://wa-bridge:8090",
		WhatsAppBridgeToken: strings.Repeat("t", 32)}
	if err := validateWhatsAppConfig(cfg); err == nil || !strings.Contains(err.Error(), "not allowed in production") {
		t.Fatalf("production must refuse the bridge, got %v", err)
	}
	cfg.AppEnv = "staging"
	if err := validateWhatsAppConfig(cfg); err != nil {
		t.Fatalf("staging may use the bridge: %v", err)
	}
}
