package mailreconcile

import (
	"context"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/google/uuid"
)

const (
	defaultInterval           = 6 * time.Hour
	defaultGrace              = 24 * time.Hour
	defaultMaxPurgesPerTenant = 100

	// minGrace impide una gracia menor que el retraso posible entre el alta de un buzon y el momento en que
	// es visible para quien pregunta por el (outbox, NATS, replicas): con menos se podria retirar el dato de un
	// buzon recien creado.
	minGrace = 10 * time.Minute
	maxGrace = 30 * 24 * time.Hour

	// DefaultBatchSize es cuantos buzones se preguntan por llamada; queda por debajo de lo que admite
	// mail-directory.
	DefaultBatchSize = 200
)

// ConfigFromEnv lee la configuracion del barrido de un servicio, con su prefijo de variables (por ejemplo
// MAIL_DAV): <PREFIX>_RECONCILE_INTERVAL (por defecto 6h; 0 lo desactiva), <PREFIX>_RECONCILE_GRACE (24h, de 10m
// a 30d) y <PREFIX>_RECONCILE_MAX_PURGES_PER_TENANT (100). Un valor fuera de rango impide arrancar.
func ConfigFromEnv(prefix string) (Config, error) {
	cfg := Config{BatchSize: DefaultBatchSize}
	var err error
	if cfg.Interval, err = config.EnvDuration(prefix+"_RECONCILE_INTERVAL", defaultInterval, 0, 30*24*time.Hour); err != nil {
		return cfg, err
	}
	if cfg.Interval > 0 && cfg.Interval < time.Minute {
		return cfg, fmt.Errorf("%s_RECONCILE_INTERVAL debe ser 0 (desactivado) o al menos 1m", prefix)
	}
	if cfg.Grace, err = config.EnvDuration(prefix+"_RECONCILE_GRACE", defaultGrace, minGrace, maxGrace); err != nil {
		return cfg, err
	}
	if cfg.MaxPurgesPerTenant, err = config.EnvInt(prefix+"_RECONCILE_MAX_PURGES_PER_TENANT", defaultMaxPurgesPerTenant, 1, 100000); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// TenantsOf adapta el recorrido de empresas activas de pkg/db.
func TenantsOf(tenantDB *db.TenantDB) Tenants { return dbTenants{tenantDB} }

type dbTenants struct{ db *db.TenantDB }

func (t dbTenants) ForEach(ctx context.Context, fn func(ctx context.Context, tenantID uuid.UUID)) error {
	return t.db.ForEachActiveTenant(ctx, func(tctx context.Context, tenantID string) {
		id, err := uuid.Parse(tenantID)
		if err != nil {
			return
		}
		fn(tctx, id)
	})
}
