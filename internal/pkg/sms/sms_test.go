package sms

import "testing"

func TestNormalizePhone(t *testing.T) {
	for in, want := range map[string]string{
		"081234567890":      "+6281234567890",
		"6281234567890":     "+6281234567890",
		"+62 812-3456-7890": "+6281234567890",
		"(0812) 3456.7890":  "+6281234567890",
		"+6591234567":       "+6591234567",
	} {
		if got, err := NormalizePhone(in); err != nil || got != want {
			t.Errorf("NormalizePhone(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "12345", "0212345678", "+62212345678", "+62812", "phone", "+0812345678"} {
		if got, err := NormalizePhone(bad); err == nil {
			t.Errorf("NormalizePhone(%q) = %q, want error", bad, got)
		}
	}
}
