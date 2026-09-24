package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
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

// DBTarget localiza la base de una empresa: la base y, si su celda no es el cluster por
// defecto, el host y puerto de esa celda. Host vacio = cluster por defecto (POSTGRES_*).
type DBTarget struct {
	Host   string
	Port   int
	DBName string
}

// Key identifica el pool: dos empresas con el mismo nombre de base en celdas distintas
// son dos bases distintas.
func (t DBTarget) Key() string {
	if t.Host == "" {
		return t.DBName
	}
	return fmt.Sprintf("%s:%d/%s", t.Host, t.Port, t.DBName)
}

// plazoApertura acota la apertura de un pool. No depende del contexto de quien la pidio primero:
// otros llamantes pueden estar esperando ese mismo resultado.
const plazoApertura = 30 * time.Second

type TenantPoolManager struct {
	mu      sync.RWMutex
	pools   map[string]*pgxpool.Pool // por DBTarget.Key()
	targets map[string]DBTarget      // tenantID -> destino (cache local)
	// aperturas deja una sola apertura en curso por destino, sin retener mu mientras se abre:
	// abrir es red, y una base de empresa inalcanzable (PgBouncer esperando server_login_retry,
	// por ejemplo) dejaba a TODAS las empresas del servicio esperando detras del cerrojo.
	aperturas singleflight.Group
	// generacion sube con CloseAll: una apertura que empezo antes no guarda su pool despues.
	generacion uint64
	// abrir crea y comprueba el pool de un destino; sustituible en pruebas.
	abrir   func(ctx context.Context, key, dsn string) (*pgxpool.Pool, error)
	baseDSN func(dbName string) string
	// cellDSN construye el DSN de una base en OTRA celda. Sin el, toda empresa se
	// busca en el cluster por defecto aunque su celda diga otra cosa.
	cellDSN func(host string, port int, dbName string) string
	logger  *zap.Logger
}

func NewTenantPoolManager(baseDSN func(string) string, logger *zap.Logger) *TenantPoolManager {
	return &TenantPoolManager{
		pools:   make(map[string]*pgxpool.Pool),
		targets: make(map[string]DBTarget),
		baseDSN: baseDSN,
		logger:  logger,
		abrir:   abrirPool,
	}
}

// SetCellDSN activa el enrutado por celda: las empresas cuya celda tenga host propio
// abren su pool contra ese host. Es opcional para que un despliegue de una sola celda
// no tenga que configurar nada.
func (m *TenantPoolManager) SetCellDSN(fn func(host string, port int, dbName string) string) {
	m.cellDSN = fn
}

// GetPoolByDBName returns (or lazily creates) a pool for the given database name in
// the default cluster.
func (m *TenantPoolManager) GetPoolByDBName(ctx context.Context, dbName string) (*pgxpool.Pool, error) {
	return m.GetPool(ctx, DBTarget{DBName: dbName})
}

// GetPool devuelve (o abre) el pool del destino. Solo esperan la apertura quienes piden ese
// mismo destino, y cada uno como mucho lo que permita su propio contexto.
func (m *TenantPoolManager) GetPool(ctx context.Context, target DBTarget) (*pgxpool.Pool, error) {
	key := target.Key()
	if pool, ok := m.poolEnCache(key); ok {
		return pool, nil
	}
	dsn := m.baseDSN(target.DBName)
	if target.Host != "" && m.cellDSN != nil {
		dsn = m.cellDSN(target.Host, target.Port, target.DBName)
	}

	resultado := m.aperturas.DoChan(key, func() (any, error) {
		if pool, ok := m.poolEnCache(key); ok {
			return pool, nil
		}
		m.mu.RLock()
		generacion := m.generacion
		m.mu.RUnlock()

		abrirCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), plazoApertura)
		defer cancel()
		pool, err := m.abrir(abrirCtx, key, dsn)
		if err != nil {
			return nil, err
		}

		m.mu.Lock()
		if m.generacion != generacion {
			m.mu.Unlock()
			pool.Close()
			return nil, fmt.Errorf("tenant pool %s: los pools se cerraron durante la apertura", key)
		}
		m.pools[key] = pool
		m.mu.Unlock()
		RegisterPoolMetrics(key, pool)
		m.logger.Info("tenant pool created", zap.String("db", key))
		return pool, nil
	})

	select {
	case r := <-resultado:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.(*pgxpool.Pool), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *TenantPoolManager) poolEnCache(key string) (*pgxpool.Pool, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pool, ok := m.pools[key]
	return pool, ok
}

func abrirPool(ctx context.Context, key, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
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
		return nil, fmt.Errorf("create tenant pool %s: %w", key, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping tenant db %s: %w", key, err)
	}
	return pool, nil
}

func (m *TenantPoolManager) cacheTarget(tenantID string, target DBTarget) {
	m.mu.Lock()
	m.targets[tenantID] = target
	m.mu.Unlock()
}

func (m *TenantPoolManager) getCachedTarget(tenantID string) (DBTarget, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.targets[tenantID]
	return v, ok
}

// Forget olvida el destino en cache de una empresa (tras moverla de celda).
func (m *TenantPoolManager) Forget(tenantID string) {
	m.mu.Lock()
	delete(m.targets, tenantID)
	m.mu.Unlock()
}

func (m *TenantPoolManager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generacion++
	for name, pool := range m.pools {
		UnregisterPoolMetrics(name)
		pool.Close()
		m.logger.Info("tenant pool closed", zap.String("db", name))
	}
	m.pools = make(map[string]*pgxpool.Pool)
	m.targets = make(map[string]DBTarget)
}

// ── TenantDB ─────────────────────────────────────────────────────────────────
// TenantDB resolves the correct tenant pool per-request by looking up db_name
// from the registry database, then delegating to TenantPoolManager.

type TenantDB struct {
	registry *pgxpool.Pool
	manager  *TenantPoolManager
	// defaultHost es el host del cluster por defecto tal como lo declararia una celda
	// (POSTGRES_HOST); vacio = ninguna celda se considera la de por defecto.
	defaultHost string
}

func NewTenantDB(registry *pgxpool.Pool, manager *TenantPoolManager) *TenantDB {
	return &TenantDB{registry: registry, manager: manager}
}

// SetDefaultHost declara que host de celda equivale al cluster por defecto.
func (t *TenantDB) SetDefaultHost(host string) { t.defaultHost = host }

// NewTenantRouting cablea el enrutado por empresa completo a partir de la configuracion
// de Postgres: pool por base en el cluster por defecto, pool por celda cuando la empresa
// vive en otra, y la equivalencia entre el host por defecto y su celda. Es lo que debe
// usar cada servicio de empresa en su main.
func NewTenantRouting(registry *pgxpool.Pool, pg config.PostgresConfig, logger *zap.Logger) (*TenantDB, *TenantPoolManager) {
	// Sin credencial propia, este servicio abre el registro y TODAS las bases de empresa con
	// la de plataforma, que es duena de las tablas de todos los servicios. Es el estado
	// anterior al reparto y no impide arrancar, pero no se deja pasar en silencio: fuera de
	// desarrollo hay que crear su rol (ops/db/tenant-service-role.sh) y publicar su
	// contrasena.
	if !pg.HasServiceCredential() && !config.DeclaredDevelopmentOrTest() {
		logger.Warn("servicio de empresa con la credencial de plataforma: crea su rol con ops/db/tenant-service-role.sh y publica REGISTRY_DB_PASSWORD y TENANT_DB_PASSWORD")
	}
	mgr := NewTenantPoolManager(pg.TenantDSN, logger)
	mgr.SetCellDSN(pg.TenantDSNAt)
	tdb := NewTenantDB(registry, mgr)
	tdb.SetDefaultHost(pg.Host)
	return tdb, mgr
}

// ResolveForTenant looks up the db_name for tenantID (cached) and returns its pool.
func (t *TenantDB) ResolveForTenant(ctx context.Context, tenantID string) (*pgxpool.Pool, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("empty tenant id")
	}
	if target, ok := t.manager.getCachedTarget(tenantID); ok {
		return t.manager.GetPool(ctx, target)
	}
	target, err := t.lookupTarget(ctx, `WHERE t.tenant_id = $1`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("resolve tenant db for %s: %w", tenantID, err)
	}
	t.manager.cacheTarget(tenantID, target)
	return t.manager.GetPool(ctx, target)
}

// targetColumns lee el destino de la empresa de la VISTA PUBLICADA del enrutado, no de las
// tablas de organization: es el unico dato del registro que un servicio de empresa necesita,
// y es lo unico que puede leer el rol de enrutado (migracion de registro 031). Una celda
// cuyo host coincide con el del cluster por defecto (POSTGRES_HOST) se trata como tal: es lo
// que permite que un despliegue de una sola celda declare su celda sin abrir un segundo pool
// por empresa.
const targetColumns = `t.db_name, COALESCE(t.db_host, ''), COALESCE(t.db_port, 0)
	   FROM organization.v_tenant_routing t `

func (t *TenantDB) lookupTarget(ctx context.Context, where string, args ...interface{}) (DBTarget, error) {
	var target DBTarget
	err := t.registry.QueryRow(ctx, `SELECT `+targetColumns+where, args...).Scan(&target.DBName, &target.Host, &target.Port)
	if err != nil {
		return DBTarget{}, err
	}
	return t.normalize(target), nil
}

// normalize deja Host vacio cuando la celda es el cluster por defecto.
func (t *TenantDB) normalize(target DBTarget) DBTarget {
	if target.Host == t.defaultHost {
		target.Host, target.Port = "", 0
	}
	return target
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
	_, pool, err := t.ResolveTenantBySlug(ctx, slug)
	return pool, err
}

// ResolveTenantBySlug es ResolveBySlug con el id de la empresa, que las rutas publicas
// necesitan para acotar sus consultas. Una empresa inexistente o no activa es IsUnknownTenant.
func (t *TenantDB) ResolveTenantBySlug(ctx context.Context, slug string) (string, *pgxpool.Pool, error) {
	if slug == "" {
		return "", nil, fmt.Errorf("empty slug")
	}
	var tenantID string
	var target DBTarget
	err := t.registry.QueryRow(ctx,
		`SELECT t.tenant_id, `+targetColumns+`WHERE t.slug = $1 AND t.status = 'active'`, slug,
	).Scan(&tenantID, &target.DBName, &target.Host, &target.Port)
	if err != nil {
		return "", nil, fmt.Errorf("resolve tenant by slug %s: %w", slug, err)
	}
	target = t.normalize(target)
	t.manager.cacheTarget(tenantID, target)
	pool, err := t.manager.GetPool(ctx, target)
	return tenantID, pool, err
}

// TenantSlug devuelve el slug de la empresa: con el se forman sus direcciones publicas.
func (t *TenantDB) TenantSlug(ctx context.Context, tenantID string) (string, error) {
	var slug string
	err := t.registry.QueryRow(ctx, `SELECT t.slug FROM organization.v_tenant_routing t WHERE t.tenant_id = $1`, tenantID).Scan(&slug)
	if err != nil {
		return "", fmt.Errorf("slug de la empresa %s: %w", tenantID, err)
	}
	return slug, nil
}

// ForEachActiveTenant fetches all active tenants from the registry and calls fn
// for each one with a context that has that tenant's pool already injected.
// Errors from fn are logged but do not stop iteration.
// Used by background workers (e.g. scheduler ticker) that need per-tenant access.
type activeTenantRow struct {
	id     string
	target DBTarget
}

func (t *TenantDB) listActiveTenants(ctx context.Context) ([]activeTenantRow, error) {
	rows, err := t.registry.Query(ctx,
		`SELECT t.tenant_id, `+targetColumns+`WHERE t.status = 'active'`,
	)
	if err != nil {
		return nil, fmt.Errorf("list active tenants: %w", err)
	}
	defer rows.Close()

	var tenants []activeTenantRow
	for rows.Next() {
		var r activeTenantRow
		if err := rows.Scan(&r.id, &r.target.DBName, &r.target.Host, &r.target.Port); err != nil {
			continue
		}
		r.target = t.normalize(r.target)
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
		pool, err := t.manager.GetPool(ctx, tr.target)
		if err != nil {
			continue
		}
		t.manager.cacheTarget(tr.id, tr.target)
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
		pool, err := t.manager.GetPool(ctx, tr.target)
		if err != nil {
			<-sem
			continue
		}
		t.manager.cacheTarget(tr.id, tr.target)
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
