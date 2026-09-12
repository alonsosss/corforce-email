package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

type Pool struct {
	*pgxpool.Pool
	logger *zap.Logger
}

func NewPool(ctx context.Context, dsn string, logger *zap.Logger) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}

	cfg.MaxConns = 10
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}

	logger.Info("database connected", zap.String("host", cfg.ConnConfig.Host))
	RegisterPoolMetrics(registryPoolName, pool)
	return &Pool{Pool: pool, logger: logger}, nil
}

// registryPoolName etiqueta al pool de la base de registro global, el unico que no
// pertenece a una empresa concreta.
const registryPoolName = "registry"

func (p *Pool) Close() {
	UnregisterPoolMetrics(registryPoolName)
	p.Pool.Close()
	p.logger.Info("database connection closed")
}

func SetSchema(ctx context.Context, pool *pgxpool.Pool, schema string) error {
	safe := sanitizeIdentifier(schema)
	_, err := pool.Exec(ctx, fmt.Sprintf("SET search_path TO %s, public", safe))
	return err
}

func sanitizeIdentifier(s string) string {
	result := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
			result = append(result, c)
		}
	}
	return string(result)
}
