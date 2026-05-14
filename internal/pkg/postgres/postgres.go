// Package postgres opens a pgx connection pool with sane defaults for
// service-oriented workloads.
//
// We connect via pgbouncer (transaction pooling) in production, so a few
// pgx features must be configured explicitly:
//   - prepared statements are cached per session, which transaction
//     pooling invalidates between checkouts. We disable the implicit
//     cache and use parameterised queries instead.
//   - the pool's max conns is small — pgbouncer is the real pool.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config controls connection establishment.
type Config struct {
	URL             string        `env:"DATABASE_URL" required:"true"`
	MaxConns        int32         `env:"DATABASE_MAX_CONNS" default:"10"`
	MinConns        int32         `env:"DATABASE_MIN_CONNS" default:"2"`
	MaxConnLifetime time.Duration `env:"DATABASE_MAX_CONN_LIFETIME" default:"30m"`
	MaxConnIdleTime time.Duration `env:"DATABASE_MAX_CONN_IDLE_TIME" default:"5m"`
	StatementCache  bool          `env:"DATABASE_STATEMENT_CACHE" default:"false"`
}

// Connect opens a pgx pool against the configured URL and verifies it
// with a ping before returning.
func Connect(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse url: %w", err)
	}

	pc.MaxConns = cfg.MaxConns
	pc.MinConns = cfg.MinConns
	pc.MaxConnLifetime = cfg.MaxConnLifetime
	pc.MaxConnIdleTime = cfg.MaxConnIdleTime

	if !cfg.StatementCache {
		// Safe for transaction-pooled pgbouncer.
		pc.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
		pc.ConnConfig.StatementCacheCapacity = 0
		pc.ConnConfig.DescriptionCacheCapacity = 0
	}

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("postgres: pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return pool, nil
}
