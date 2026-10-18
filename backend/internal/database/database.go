package database

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect establishes a connection pool to PostgreSQL.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("unable to parse database URL: %w", err)
	}

	// Connection pool settings
	config.MaxConns = 20
	config.MinConns = 2
	config.MaxConnLifetime = 30 * time.Minute
	config.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("unable to create connection pool: %w", err)
	}

	// Verify the connection with retries for startup readiness
	var pingErr error
	for attempt := 1; attempt <= 15; attempt++ {
		pingErr = pool.Ping(ctx)
		if pingErr == nil {
			break
		}
		if attempt < 15 {
			slog.Warn("database ping failed, retrying...", "attempt", attempt, "error", pingErr)
			select {
			case <-ctx.Done():
				pool.Close()
				return nil, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	if pingErr != nil {
		pool.Close()
		return nil, fmt.Errorf("unable to ping database after retries: %w", pingErr)
	}

	slog.Info("connected to PostgreSQL",
		"max_conns", config.MaxConns,
		"min_conns", config.MinConns,
	)

	return pool, nil
}
