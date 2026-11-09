package main

import (
	"errors"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	connectAttempts = 30
	connectWait     = 2 * time.Second
	// pgCannotConnectNow is reported while PostgreSQL is still starting up.
	pgCannotConnectNow = "57P03"
)

// retryConnect waits out a database that is still starting after a host reboot. An answer from the
// server, such as a bad password or unknown database, will not fix itself and fails at once.
func retryConnect[T any](connect func() (T, error), attempts int, wait time.Duration, sleep func(time.Duration)) (T, error) {
	var conn T
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		if conn, err = connect(); err == nil {
			return conn, nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code != pgCannotConnectNow {
			return conn, err
		}
		if attempt < attempts {
			log.Printf("[MIGRATE] database not ready (attempt %d/%d), retrying", attempt, attempts)
			sleep(wait)
		}
	}
	return conn, err
}
