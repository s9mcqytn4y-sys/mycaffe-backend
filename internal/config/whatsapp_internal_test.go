package config

import (
	"strings"
	"testing"
)

// The whatsmeow bridge is accepted as primary WhatsApp driver when properly configured.
func TestValidateWhatsAppConfigAcceptsBridgeInAllEnvs(t *testing.T) {
	token := strings.Repeat("s", 32)
	cfg := &Config{AppEnv: "production", WhatsAppDriver: "bridge", WhatsAppBridgeURL: "http://wa-bridge:8090", WhatsAppBridgeToken: token}
	if err := validateWhatsAppConfig(cfg); err != nil {
		t.Fatalf("production should accept configured bridge, got: %v", err)
	}

	cfgPlaceholder := &Config{AppEnv: "production", WhatsAppDriver: "bridge", WhatsAppBridgeURL: "http://wa-bridge:8090", WhatsAppBridgeToken: "placeholder_" + strings.Repeat("x", 20)}
	if err := validateWhatsAppConfig(cfgPlaceholder); err == nil || !strings.Contains(err.Error(), "placeholder credentials") {
		t.Fatalf("production must fail closed on placeholder token, got %v", err)
	}

	cfgNoURL := &Config{AppEnv: "production", WhatsAppDriver: "bridge", WhatsAppBridgeURL: "", WhatsAppBridgeToken: token}
	if err := validateWhatsAppConfig(cfgNoURL); err == nil {
		t.Fatalf("must fail closed on empty URL")
	}
}
