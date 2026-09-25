// Package keyrotation re-cifra bajo la llave activa de MAIL_ENCRYPTION_KEY lo que un servicio guarda
// cifrado con una llave ya retirada (MAIL_ENCRYPTION_KEYS_OLD). Cada servicio declara sus columnas
// cifradas y los datos autenticados de cada una; el paquete las recorre (crypto.RotateStore), en su
// base o en la de cada empresa, y publica lo hecho en el registro y en Prometheus con el mismo
// formato en todos. El procedimiento de la rotacion: docs/Operacion_Despliegue.md, seccion 2.
package keyrotation

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// Column es una columna bytea cifrada de una tabla con clave uuid. Implementa
// crypto.SealedStore[uuid.UUID]: lee por lotes en orden de clave y sustituye solo si la fila sigue
// guardando lo leido. Sin pool, usa el de la empresa que lleva el contexto (db.WithPool).
type Column struct {
	pool      *pgxpool.Pool
	selectSQL string
	updateSQL string
}

var identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// NewColumn describe la columna column de table ("esquema.tabla"), con clave key. Los nombres son
// constantes del adaptador de cada servicio, nunca datos: uno mal formado es un error de programa.
func NewColumn(pool *pgxpool.Pool, table, key, column string) (*Column, error) {
	schema, name, ok := splitTable(table)
	if !ok || !identifier.MatchString(key) || !identifier.MatchString(column) {
		return nil, fmt.Errorf("keyrotation: columna mal descrita %q.%q (%q)", table, column, key)
	}
	t := pgx.Identifier{schema, name}.Sanitize()
	k := pgx.Identifier{key}.Sanitize()
	c := pgx.Identifier{column}.Sanitize()
	return &Column{
		pool:      pool,
		selectSQL: fmt.Sprintf(`SELECT %s, %s FROM %s WHERE %s IS NOT NULL AND %s > $1 ORDER BY %s LIMIT $2`, k, c, t, c, k, k),
		updateSQL: fmt.Sprintf(`UPDATE %s SET %s = $3 WHERE %s = $1 AND %s = $2`, t, c, k, c),
	}, nil
}

// MustColumn es NewColumn para las descripciones fijas de un adaptador.
func MustColumn(pool *pgxpool.Pool, table, key, column string) *Column {
	c, err := NewColumn(pool, table, key, column)
	if err != nil {
		panic(err)
	}
	return c
}

func splitTable(table string) (schema, name string, ok bool) {
	for i := 0; i < len(table); i++ {
		if table[i] == '.' {
			schema, name = table[:i], table[i+1:]
			return schema, name, identifier.MatchString(schema) && identifier.MatchString(name)
		}
	}
	return "", "", false
}

func (c *Column) poolFor(ctx context.Context) (*pgxpool.Pool, error) {
	if c.pool != nil {
		return c.pool, nil
	}
	if p, ok := db.PoolFromCtx(ctx); ok {
		return p, nil
	}
	return nil, errors.New("keyrotation: sin base en el contexto")
}

func (c *Column) SealedAfter(ctx context.Context, after uuid.UUID, limit int) ([]crypto.SealedRecord[uuid.UUID], error) {
	pool, err := c.poolFor(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, c.selectSQL, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []crypto.SealedRecord[uuid.UUID]
	for rows.Next() {
		var rec crypto.SealedRecord[uuid.UUID]
		if err := rows.Scan(&rec.Key, &rec.Sealed); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (c *Column) ReplaceSealed(ctx context.Context, key uuid.UUID, prev, next []byte) (bool, error) {
	pool, err := c.poolFor(ctx)
	if err != nil {
		return false, err
	}
	tag, err := pool.Exec(ctx, c.updateSQL, key, prev, next)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// batch son las filas que se leen por consulta.
const batch = 200

// Target es una columna cifrada de la base del servicio (registro o celda).
type Target struct {
	Store crypto.SealedStore[uuid.UUID]
	// AAD son los datos autenticados con que se cifro la fila de esa clave; nil si se cifro sin.
	AAD func(key uuid.UUID) []byte
}

// TenantTarget es una columna cifrada de la base de cada empresa.
type TenantTarget struct {
	Column *Column
	// AAD son los datos autenticados de la fila key de la empresa tenant; nil si se cifro sin.
	AAD func(tenant, key uuid.UUID) []byte
}

// Result es una pasada completa sobre lo cifrado de un servicio.
type Result struct {
	crypto.RotationReport
	// UnreachedTenants son las empresas cuya base no se pudo recorrer entera: lo que guarden puede
	// seguir bajo la llave retirada.
	UnreachedTenants []string
}

// Outstanding es lo que impide retirar la llave vieja: los datos que no abre ninguna llave mas
// una por cada empresa que no se pudo recorrer.
func (r Result) Outstanding() int { return r.Pending + len(r.UnreachedTenants) }

func (r *Result) add(o crypto.RotationReport) {
	r.Examined += o.Examined
	r.Rotated += o.Rotated
	r.Changed += o.Changed
	r.Pending += o.Pending
}

func noAAD(uuid.UUID) []byte { return nil }

// Pass recorre las columnas de la base del servicio.
func Pass(ctx context.Context, kr *crypto.KeyRing, targets ...Target) (Result, error) {
	var res Result
	for _, t := range targets {
		aad := t.AAD
		if aad == nil {
			aad = noAAD
		}
		rep, err := crypto.RotateStore(ctx, kr, t.Store, uuid.Nil, batch, aad)
		res.add(rep)
		if err != nil {
			return res, err
		}
	}
	return res, nil
}

// TenantPass recorre las columnas en la base de todas las empresas, en cualquier estado, con el
// limite de concurrencia y el plazo por empresa dados. Una empresa que falla no para a las demas:
// queda en UnreachedTenants y se registra.
func TenantPass(ctx context.Context, kr *crypto.KeyRing, tenants *db.TenantDB, concurrency int, perTenant time.Duration,
	logger *zap.Logger, targets ...TenantTarget) (Result, error) {
	var (
		mu  sync.Mutex
		res Result
	)
	failed, err := tenants.ForEachTenantDatabase(ctx, concurrency, perTenant, func(tctx context.Context, tenantID string) error {
		tenant, err := uuid.Parse(tenantID)
		if err != nil {
			return err
		}
		for _, t := range targets {
			aad := noAAD
			if t.AAD != nil {
				aad = func(key uuid.UUID) []byte { return t.AAD(tenant, key) }
			}
			rep, err := crypto.RotateStore(tctx, kr, t.Column, uuid.Nil, batch, aad)
			mu.Lock()
			res.add(rep)
			mu.Unlock()
			if err != nil {
				logger.Warn("rotacion de MAIL_ENCRYPTION_KEY: no se pudo recorrer la base de la empresa",
					zap.String("tenant_id", tenantID), zap.Error(err))
				return err
			}
		}
		return nil
	})
	res.UnreachedTenants = failed
	return res, err
}

// Metrics publica las pasadas de un servicio: <prefix>_secrets_reencrypted_total y
// <prefix>_secrets_pending_reencryption.
type Metrics struct {
	reencrypted prometheus.Counter
	pending     prometheus.Gauge
}

// NewMetrics registra las dos metricas en el registro por defecto (el que sirve pkg/server en
// /metrics). what describe lo cifrado en la ayuda.
func NewMetrics(prefix, what string) *Metrics {
	m := &Metrics{
		reencrypted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: prefix + "_secrets_reencrypted_total",
			Help: what + " re-cifrados bajo la llave activa de MAIL_ENCRYPTION_KEY.",
		}),
		pending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: prefix + "_secrets_pending_reencryption",
			Help: what + " que la ultima pasada completa de rotacion no pudo dejar bajo la llave activa (sin llave que los abra, o en una base de empresa que no se pudo recorrer). Con uno solo, retirar la llave vieja los deja ilegibles.",
		}),
	}
	prometheus.MustRegister(m.reencrypted, m.pending)
	return m
}

// Every es el ritmo de las pasadas mientras quedan llaves retiradas en el anillo.
const Every = time.Hour

// Report publica una pasada: suma lo re-cifrado aunque se interrumpiera y, si termino, fija los
// pendientes y registra "rotacion de MAIL_ENCRYPTION_KEY en <where>: N re-cifrados, P pendientes",
// la linea que espera quien rota. Nunca registra un dato cifrado ni en claro.
func Report(ctx context.Context, where string, res Result, err error, m *Metrics, logger *zap.Logger) {
	if m != nil {
		m.reencrypted.Add(float64(res.Rotated))
	}
	if err != nil {
		if ctx.Err() == nil {
			logger.Warn(fmt.Sprintf("rotacion de MAIL_ENCRYPTION_KEY en %s interrumpida; se reintenta en la proxima pasada", where),
				zap.Int("recifrados", res.Rotated), zap.Error(err))
		}
		return
	}
	if m != nil {
		m.pending.Set(float64(res.Outstanding()))
	}
	logger.Info(fmt.Sprintf("rotacion de MAIL_ENCRYPTION_KEY en %s: %d re-cifrados, %d pendientes", where, res.Rotated, res.Outstanding()),
		zap.Int("revisados", res.Examined), zap.Int("recifrados", res.Rotated),
		zap.Int("cambiados_entre_tanto", res.Changed), zap.Int("ilegibles", res.Pending),
		zap.Strings("empresas_sin_recorrer", res.UnreachedTenants))
}

// Run re-cifra con pass al arrancar y despues cada Every, solo si el anillo tiene llaves retiradas:
// sin ellas no hay nada que rotar y no corre nada. Vuelve al cancelarse ctx.
func Run(ctx context.Context, kr *crypto.KeyRing, where string, pass func(context.Context) (Result, error), m *Metrics, logger *zap.Logger) {
	if !kr.HasOldKeys() {
		return
	}
	t := time.NewTicker(Every)
	defer t.Stop()
	for {
		res, err := pass(ctx)
		Report(ctx, where, res, err, m, logger)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
