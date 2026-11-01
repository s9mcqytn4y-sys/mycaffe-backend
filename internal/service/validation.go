package service

import (
	"fmt"
	"sort"
	"strings"
)

// ValidationError carries field-level messages (Bahasa Indonesia) for HTTP 422 responses.
// Keys are the JSON field names of the request, so forms can highlight the right input.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return "validation failed: " + strings.Join(keys, ", ")
}

// fieldErrors collects messages; Err returns nil when nothing failed.
type fieldErrors map[string]string

func (f fieldErrors) add(field, msg string) {
	if _, exists := f[field]; !exists {
		f[field] = msg
	}
}

func (f fieldErrors) err() error {
	if len(f) == 0 {
		return nil
	}
	return &ValidationError{Fields: f}
}

func (f fieldErrors) text(field, value string, min, max int) string {
	value = strings.TrimSpace(value)
	switch {
	case len([]rune(value)) < min && min == 1:
		f.add(field, "wajib diisi")
	case len([]rune(value)) < min:
		f.add(field, fmt.Sprintf("minimal %d karakter", min))
	case len([]rune(value)) > max:
		f.add(field, fmt.Sprintf("maksimal %d karakter", max))
	}
	return value
}

func (f fieldErrors) intRange(field string, v, min, max int64) {
	if v < min || v > max {
		f.add(field, fmt.Sprintf("harus antara %d dan %d", min, max))
	}
}

func (f fieldErrors) oneOf(field, v string, allowed ...string) {
	for _, a := range allowed {
		if v == a {
			return
		}
	}
	f.add(field, "harus salah satu dari: "+strings.Join(allowed, ", "))
}

// ConflictError is a uniqueness clash on one field (HTTP 409).
type ConflictError struct {
	Field   string
	Message string
}

func (e *ConflictError) Error() string { return "conflict on " + e.Field + ": " + e.Message }
