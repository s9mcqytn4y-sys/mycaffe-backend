package main

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestRetryConnect(t *testing.T) {
	refused := errors.New("connection refused")
	tests := []struct {
		name      string
		errs      []error
		wantCalls int
		wantErr   bool
	}{
		{name: "connects first time", errs: []error{nil}, wantCalls: 1},
		{name: "waits for a refused connection", errs: []error{refused, refused, nil}, wantCalls: 3},
		{name: "waits while postgres starts", errs: []error{fmt.Errorf("ping: %w", &pgconn.PgError{Code: pgCannotConnectNow}), nil}, wantCalls: 2},
		{name: "stops on bad credentials", errs: []error{fmt.Errorf("ping: %w", &pgconn.PgError{Code: "28P01"})}, wantCalls: 1, wantErr: true},
		{name: "gives up after all attempts", errs: []error{refused, refused, refused}, wantCalls: 3, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls, sleeps := 0, 0
			_, err := retryConnect(func() (int, error) {
				calls++
				return calls, tt.errs[calls-1]
			}, 3, time.Second, func(time.Duration) { sleeps++ })
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if calls != tt.wantCalls {
				t.Fatalf("calls = %d, want %d", calls, tt.wantCalls)
			}
			if tt.name == "gives up after all attempts" && sleeps != 2 {
				t.Fatalf("sleeps = %d, want 2", sleeps)
			}
		})
	}
}
