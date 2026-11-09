package main

import (
	"fmt"
	"strings"
)

// render turns a template name and its parameters into the text the demo sends. Template
// "struk_digital" mirrors the approved WhatsApp Cloud API template (decision W14) so a demo
// receipt reads exactly like production; "text" sends the first parameter as is.
func render(template string, params []string) (string, error) {
	switch template {
	case "struk_digital":
		if len(params) != 3 {
			return "", fmt.Errorf("struk_digital needs 3 parameters, got %d", len(params))
		}
		return fmt.Sprintf("Terima kasih sudah berbelanja di %s. Struk digital pesanan %s bisa dilihat di %s. Tautan berlaku 90 hari.",
			params[0], params[1], params[2]), nil
	case "tagihan_invoice":
		if len(params) != 5 {
			return "", fmt.Errorf("tagihan_invoice needs 5 parameters, got %d", len(params))
		}
		return fmt.Sprintf("%s: invoice %s sebesar %s %s (%s). Mohon abaikan bila sudah dibayar.",
			params[0], params[1], params[2], params[3], params[4]), nil
	case "text":
		if len(params) != 1 || strings.TrimSpace(params[0]) == "" {
			return "", fmt.Errorf("text needs 1 non-empty parameter")
		}
		return params[0], nil
	}
	return "", fmt.Errorf("unknown template %q", template)
}
