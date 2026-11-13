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
	case "struk_digital", "cassy_order_receipt":
		if len(params) == 3 {
			return fmt.Sprintf("Terima kasih sudah memesan di %s via Cassy. Struk digital pesanan %s dapat dilihat di %s. Tautan berlaku 90 hari.",
				params[0], params[1], params[2]), nil
		}
		if len(params) == 6 {
			return fmt.Sprintf("Halo %s, terima kasih sudah memesan di %s. Pesanan %s senilai %s (%s) selesai. Struk digital: %s",
				params[0], params[2], params[1], params[3], params[4], params[5]), nil
		}
		return "", fmt.Errorf("%s needs 3 or 6 parameters, got %d", template, len(params))
	case "tagihan_invoice":
		if len(params) != 5 {
			return "", fmt.Errorf("tagihan_invoice needs 5 parameters, got %d", len(params))
		}
		return fmt.Sprintf("[Cassy Billing] %s: tagihan %s sebesar %s %s (%s). Mohon abaikan bila sudah dibayar.",
			params[0], params[1], params[2], params[3], params[4]), nil
	case "text":
		if len(params) != 1 || strings.TrimSpace(params[0]) == "" {
			return "", fmt.Errorf("text needs 1 non-empty parameter")
		}
		return params[0], nil
	}
	return "", fmt.Errorf("unknown template %q", template)
}
