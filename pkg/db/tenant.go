package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// ── Context pool key ──────────────────────────────────────────────────────────

type poolCtxKey struct{}

// WithPool injects a resolved pool into the context.
func WithPool(ctx context.Context, pool *pgxpool.Pool) context.Context {
	return context.WithValue(ctx, poolCtxKey{}, pool)
}

// PoolFromCtx extracts the tenant pool from the context.
func PoolFromCtx(ctx context.Context) (*pgxpool.Pool, bool) {
	pool, ok := ctx.Value(poolCtxKey{}).(*pgxpool.Pool)
	return pool, ok
}

// ── Transaction context key ───────────────────────────────────────────────────

type txCtxKey struct{}

// WithTx injects an active pgx.Tx into the context so that ContextPool methods
// route all SQL through the transaction instead of the pool.
func WithTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txCtxKey{}, tx)
}

func txFromCtx(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txCtxKey{}).(pgx.Tx)
	return tx, ok
}

// HasTx indica si el contexto ya transporta una transaccion activa. Permite que los
// helpers de transaccion sean reentrantes (no abrir una transaccion anidada).
func HasTx(ctx context.Context) bool {
	_, ok := txFromCtx(ctx)
	return ok
}

// ── ContextPool — drop-in replacement for *pgxpool.Pool ──────────────────────
// Repos keep one *ContextPool (stateless singleton). Each DB call resolves the
// actual pool (or an active transaction) from the request context — no method
// body changes required in individual repos.

type ContextPool struct{}

func (p *ContextPool) Exec(ctx context.Context, sql string, arguments ...interface{}) (pgconn.CommandTag, error) {
	if tx, ok := txFromCtx(ctx); ok {
		return tx.Exec(ctx, sql, arguments...)
	}
	pool, ok := PoolFromCtx(ctx)
	if !ok {
		return pgconn.CommandTag{}, fmt.Errorf("no tenant pool in context")
	}
	return pool.Exec(ctx, sql, arguments...)
}

func (p *ContextPool) Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	if tx, ok := txFromCtx(ctx); ok {
		return tx.Query(ctx, sql, args...)
	}
	pool, ok := PoolFromCtx(ctx)
	if !ok {
		return nil, fmt.Errorf("no tenant pool in context")
	}
	return pool.Query(ctx, sql, args...)
}

func (p *ContextPool) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	if tx, ok := txFromCtx(ctx); ok {
		return tx.QueryRow(ctx, sql, args...)
	}
	pool, ok := PoolFromCtx(ctx)
	if !ok {
		return errRow{fmt.Errorf("no tenant pool in context")}
	}
	return pool.QueryRow(ctx, sql, args...)
}

// Transact begins a transaction, injects it into the context via WithTx, runs
// fn, and commits on success or rolls back on any error.
func (p *ContextPool) Transact(ctx context.Context, fn func(ctx context.Context) error) error {
	tx, err := p.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	txCtx := WithTx(ctx, tx)
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(txCtx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RolRLS es el rol sin privilegios de dueño bajo el que corren las transacciones que
// dependen de politicas RLS. Lo crea la migracion que introduce la primera politica.
//
// Postgres EXIME al dueño de la tabla de sus propias politicas, y la aplicacion conecta
// como dueña: con FORCE ROW LEVEL SECURITY y una politica USING(false) el usuario de la
// aplicacion sigue leyendo todas las filas; el mismo SELECT bajo un rol que no es dueño
// devuelve cero. Sin cambiar de rol, una politica RLS es decorativa.
//
// Se cambia de rol DENTRO de la transaccion (SET LOCAL) y no de credencial: conectar con
// otro usuario exige una contraseña que guardar, rotar y enseñar a pgbouncer. El
// sombrero por transaccion da la misma proteccion sin superficie de credencial.
//
// Tampoco hace falta FORCE ROW LEVEL SECURITY: forzarlas dejaria sujeto tambien al
// dueño, que es quien hace pg_dump, y los respaldos de esas tablas saldrian vacios.
const RolRLS = "mail_app"

// TransactRLS es Transact para el codigo cuyas tablas tienen politicas RLS: ademas de la
// identidad, cambia a un rol que SI queda sujeto a ellas.
//
// Es opt-in y no el comportamiento por defecto a proposito. El rol necesita permisos
// explicitos sobre cada tabla que toque, y aplicarlo a todas las transacciones de la plataforma
// convertiria cualquier permiso olvidado en un "permission denied" en produccion, a cambio
// de nada en los esquemas que no tienen ninguna politica.
func (p *ContextPool) TransactRLS(ctx context.Context, fn func(ctx context.Context) error) error {
	tx, err := p.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	txCtx := WithTx(ctx, tx)
	defer func() { _ = tx.Rollback(ctx) }()
	// Solo se cambia de rol cuando hay un usuario detras de la peticion.
	//
	// Las pasadas de fondo -el generador de recurrentes, los resumenes diarios- corren sin
	// usuario, y las politicas de escritura de planning exigen identidad: bajo el rol
	// sujeto a RLS dejarian de poder insertar y las tareas recurrentes se detendrian sin
	// que nadie lo notara hasta echarlas en falta. Es el mismo criterio que ya sigue el
	// alcance por empresa en los trabajos de fondo: un proceso interno sin usuario no se acota.
	if userID, _, _ := sessionFlags(ctx); userID != "" {
		// SET LOCAL: vuelve al rol de siempre al terminar la transaccion, la confirme o no.
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+RolRLS); err != nil {
			return fmt.Errorf("cambiar al rol sujeto a RLS: %w", err)
		}
	}
	if err := fn(txCtx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *ContextPool) Begin(ctx context.Context) (pgx.Tx, error) {
	pool, ok := PoolFromCtx(ctx)
	if !ok {
		return nil, fmt.Errorf("no tenant pool in context")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	userID, tenantID, isPrivileged := sessionFlags(ctx)
	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_user_id', $1, true), set_config('app.current_tenant_id', $2, true), set_config('app.is_privileged', $3, true)",
		userID, tenantID, isPrivileged,
	); err != nil {
		_ = tx.Rollback(ctx)
		return nil, fmt.Errorf("set tenant session context: %w", err)
	}
	return tx, nil
}

// errRow satisfies pgx.Row and always returns an error on Scan.
type errRow struct{ err error }

func (r errRow) Scan(dest ...interface{}) error { return r.err }

// ── TenantPoolManager ─────────────────────────────────────────────────────────

type TenantPoolManager struct {
	mu      sync.RWMutex
	pools   map[string]*pgxpool.Pool // keyed by db_name
	dbNames map[string]string        // tenantID → db_name (local cache)
	baseDSN func(dbName string) string
	logger  *zap.Logger
}

func NewTenantPoolManager(baseDSN func(string) string, logger *zap.Logger) *TenantPoolManager {
	return &TenantPoolManager{
		pools:   make(map[string]*pgxpool.Pool),
		dbNames: make(map[string]string),
		baseDSN: baseDSN,
		logger:  logger,
	}
}

// GetPoolByDBName returns (or lazily creates) a pool for the given database name.
func (m *TenantPoolManager) GetPoolByDBName(ctx context.Context, dbName string) (*pgxpool.Pool, error) {
	m.mu.RLock()
	if pool, ok := m.pools[dbName]; ok {
		m.mu.RUnlock()
		return pool, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if pool, ok := m.pools[dbName]; ok {
		return pool, nil
	}

	cfg, err := pgxpool.ParseConfig(m.baseDSN(dbName))
	if err != nil {
		return nil, fmt.Errorf("parse tenant dsn: %w", err)
	}
	cfg.MaxConns = 10
	// MinConns=0 so an idle tenant does not hold a connection open forever;
	// combined with MaxConnIdleTime, idle connections are reaped, keeping the
	// aggregate connection count (tenants x services) well under Postgres limits.
	// New requests reopen connections on demand.
	cfg.MinConns = 0
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.MaxConnLifetime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create tenant pool %s: %w", dbName, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping tenant db %s: %w", dbName, err)
	}

	m.pools[dbName] = pool
	RegisterPoolMetrics(dbName, pool)
	m.logger.Info("tenant pool created", zap.String("db", dbName))
	return pool, nil
}

func (m *TenantPoolManager) cacheDBName(tenantID, dbName string) {
	m.mu.Lock()
	m.dbNames[tenantID] = dbName
	m.mu.Unlock()
}

func (m *TenantPoolManager) getCachedDBName(tenantID string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.dbNames[tenantID]
	return v, ok
}

func (m *TenantPoolManager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, pool := range m.pools {
		UnregisterPoolMetrics(name)
		pool.Close()
		m.logger.Info("tenant pool closed", zap.String("db", name))
	}
	m.pools = make(map[string]*pgxpool.Pool)
	m.dbNames = make(map[string]string)
}

// ── TenantDB ─────────────────────────────────────────────────────────────────
// TenantDB resolves the correct tenant pool per-request by looking up db_name
// from the registry database, then delegating to TenantPoolManager.

type TenantDB struct {
	registry *pgxpool.Pool
	manager  *TenantPoolManager
}

func NewTenantDB(registry *pgxpool.Pool, manager *TenantPoolManager) *TenantDB {
	return &TenantDB{registry: registry, manager: manager}
}

// ResolveForTenant looks up the db_name for tenantID (cached) and returns its pool.
func (t *TenantDB) ResolveForTenant(ctx context.Context, tenantID string) (*pgxpool.Pool, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("empty tenant id")
	}
	if dbName, ok := t.manager.getCachedDBName(tenantID); ok {
		return t.manager.GetPoolByDBName(ctx, dbName)
	}
	var dbName string
	err := t.registry.QueryRow(ctx,
		`SELECT db_name FROM organization.tenants WHERE id = $1`, tenantID,
	).Scan(&dbName)
	if err != nil {
		return nil, fmt.Errorf("resolve tenant db for %s: %w", tenantID, err)
	}
	t.manager.cacheDBName(tenantID, dbName)
	return t.manager.GetPoolByDBName(ctx, dbName)
}

// IsUnknownTenant indica que el fallo de ResolveForTenant es DEFINITIVO: el tenant no
// figura en el registro. Es la diferencia entre un evento que nunca se va a poder
// procesar y una base que no responde ahora.
//
// Importa en los consumidores de JetStream: reintentar sin ack un evento cuyo tenant no
// existe lo reentrega para siempre (un mensaje envenenado que gira cada pocos segundos
// consumiendo CPU y llenando el log). Esos eventos se descartan con ack; los transitorios
// se reintentan.
func IsUnknownTenant(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

// ResolveBySlug resuelve el pool de una empresa activa por su slug (rutas publicas
// que identifican a la empresa por nombre y no por token).
func (t *TenantDB) ResolveBySlug(ctx context.Context, slug string) (*pgxpool.Pool, error) {
	if slug == "" {
		return nil, fmt.Errorf("empty slug")
	}
	var tenantID, dbName string
	err := t.registry.QueryRow(ctx,
		`SELECT id, db_name FROM organization.tenants WHERE slug = $1 AND status = 'active'`, slug,
	).Scan(&tenantID, &dbName)
	if err != nil {
		return nil, fmt.Errorf("resolve tenant by slug %s: %w", slug, err)
	}
	t.manager.cacheDBName(tenantID, dbName)
	return t.manager.GetPoolByDBName(ctx, dbName)
}

// ForEachActiveTenant fetches all active tenants from the registry and calls fn
// for each one with a context that has that tenant's pool already injected.
// Errors from fn are logged but do not stop iteration.
// Used by background workers (e.g. scheduler ticker) that need per-tenant access.
type activeTenantRow struct{ id, dbName string }

func (t *TenantDB) listActiveTenants(ctx context.Context) ([]activeTenantRow, error) {
	rows, err := t.registry.Query(ctx,
		`SELECT id, db_name FROM organization.tenants WHERE status = 'active'`,
	)
	if err != nil {
		return nil, fmt.Errorf("list active tenants: %w", err)
	}
	defer rows.Close()

	var tenants []activeTenantRow
	for rows.Next() {
		var r activeTenantRow
		if err := rows.Scan(&r.id, &r.dbName); err != nil {
			continue
		}
		tenants = append(tenants, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan tenants: %w", err)
	}
	return tenants, nil
}

func (t *TenantDB) ForEachActiveTenant(ctx context.Context, fn func(ctx context.Context, tenantID string)) error {
	tenants, err := t.listActiveTenants(ctx)
	if err != nil {
		return err
	}
	for _, tr := range tenants {
		pool, err := t.manager.GetPoolByDBName(ctx, tr.dbName)
		if err != nil {
			continue
		}
		t.manager.cacheDBName(tr.id, tr.dbName)
		// El contexto lleva el pool Y la empresa. Sin lo segundo, cualquier
		// consulta a una tabla con RLS devuelve CERO filas y sin error: el GUC
		// app.current_tenant_id viajaría vacío. Es el fallo más traicionero de
		// este patrón, porque un trabajo de fondo mudo parece un trabajo sin
		// nada que hacer.
		fn(WithTenant(ctx, pool, tr.id), tr.id)
	}
	return nil
}

// WithTenant deja el contexto listo para consultar la base de una empresa desde
// un trabajo de fondo: el pool y la identidad de empresa que leen las políticas.
func WithTenant(ctx context.Context, pool *pgxpool.Pool, tenantID string) context.Context {
	return middleware.WithTenantID(WithPool(ctx, pool), tenantID)
}

// ForEachActiveTenantConcurrent procesa los tenants activos en paralelo con un límite de
// concurrencia (worker pool) y un timeout independiente por tenant. Evita que el número de
// tenants degrade la latencia del scheduler: con N tenants el tiempo total deja de ser la
// suma de todos y pasa a ser ~el del lote más lento. Un panic o timeout de un tenant no
// afecta a los demás.
func (t *TenantDB) ForEachActiveTenantConcurrent(ctx context.Context, concurrency int, perTenant time.Duration, fn func(ctx context.Context, tenantID string)) error {
	tenants, err := t.listActiveTenants(ctx)
	if err != nil {
		return err
	}
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, tr := range tenants {
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case sem <- struct{}{}:
		}
		pool, err := t.manager.GetPoolByDBName(ctx, tr.dbName)
		if err != nil {
			<-sem
			continue
		}
		t.manager.cacheDBName(tr.id, tr.dbName)
		wg.Add(1)
		go func(tr activeTenantRow, pool *pgxpool.Pool) {
			defer wg.Done()
			defer func() { <-sem }()
			// recover evita que el panic de un tenant tumbe todo el ciclo del scheduler.
			defer func() { _ = recover() }()
			// Mismo motivo que en la variante secuencial: sin la empresa en el
			// contexto, las tablas con RLS devuelven cero filas en silencio.
			tctx := WithTenant(ctx, pool, tr.id)
			if perTenant > 0 {
				var cancel context.CancelFunc
				tctx, cancel = context.WithTimeout(tctx, perTenant)
				defer cancel()
			}
			fn(tctx, tr.id)
		}(tr, pool)
	}
	wg.Wait()
	return nil
}
