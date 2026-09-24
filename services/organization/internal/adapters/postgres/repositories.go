package postgres

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TenantRepo struct {
	pool *pgxpool.Pool
}

func NewTenantRepo(pool *pgxpool.Pool) *TenantRepo {
	return &TenantRepo{pool: pool}
}

const tenantColumns = `id, slug, name, db_name, status, cell_id, settings, created_at, updated_at`

func scanTenant(row pgx.Row) (*domain.Tenant, error) {
	t := &domain.Tenant{}
	if err := row.Scan(&t.ID, &t.Slug, &t.Name, &t.DBName, &t.Status, &t.CellID, &t.Settings, &t.CreatedAt, &t.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrTenantNotFound
		}
		return nil, err
	}
	return t, nil
}

func (r *TenantRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error) {
	return scanTenant(r.pool.QueryRow(ctx,
		`SELECT `+tenantColumns+` FROM organization.tenants WHERE id = $1`, id))
}

func (r *TenantRepo) GetBySlug(ctx context.Context, slug string) (*domain.Tenant, error) {
	return scanTenant(r.pool.QueryRow(ctx,
		`SELECT `+tenantColumns+` FROM organization.tenants WHERE slug = $1`, slug))
}

func (r *TenantRepo) List(ctx context.Context, offset, limit int) ([]*domain.Tenant, int64, error) {
	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM organization.tenants`).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := r.pool.Query(ctx,
		`SELECT `+tenantColumns+` FROM organization.tenants ORDER BY created_at DESC LIMIT $1 OFFSET $2`,
		limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var tenants []*domain.Tenant
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, 0, err
		}
		tenants = append(tenants, t)
	}
	return tenants, total, rows.Err()
}

func (r *TenantRepo) Update(ctx context.Context, t *domain.Tenant) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE organization.tenants SET name=$1, status=$2, settings=$3 WHERE id=$4`,
		t.Name, t.Status, t.Settings, t.ID,
	)
	return err
}

// ── Aprovisionamiento de bases de tenant ─────────────────────────────────────

type MigrationFile struct {
	Name string
	SQL  string
}

// tenantMigrationLockKey es la clave del advisory lock que serializa la aplicacion de
// migraciones sobre una base de tenant. Sin el, dos replicas (o un arranque que coincide
// con la ejecucion manual del endpoint) aplicarian la misma migracion en paralelo.
const tenantMigrationLockKey int64 = 0x6d696772 // "migr"

// baselineMarker se inserta en schema_migrations cuando una base preexistente se marca
// como migrada sin ejecutar el SQL. Deja rastro visible en el endpoint de estado: un
// tenant baselineado pudo quedarse sin las migraciones posteriores a su creacion.
const baselineMarker = "__baseline__"

type DBProvisioner struct {
	pool        *pgxpool.Pool
	migrations  []MigrationFile
	directDSNFn func(host string, port int, dbName string) string
	// defaultHost es el host del cluster del registro: una celda con ese host se
	// aprovisiona por el pool del registro; cualquier otra, por su propio host.
	defaultHost string
}

// NewDBProvisioner recibe la funcion que construye el DSN DIRECTO (sin pgbouncer) de
// cada base en su celda: los advisory locks exigen semantica de sesion.
func NewDBProvisioner(pool *pgxpool.Pool, migrations []MigrationFile, directDSNFn func(string, int, string) string, defaultHost string) *DBProvisioner {
	return &DBProvisioner{pool: pool, migrations: migrations, directDSNFn: directDSNFn, defaultHost: defaultHost}
}

// remote indica si el destino vive en un cluster distinto al del registro.
func (p *DBProvisioner) remote(target domain.DBTarget) bool {
	return target.Host != "" && target.Host != p.defaultHost
}

// adminExec ejecuta una sentencia de administracion (CREATE/DROP DATABASE) en el cluster
// del destino: por el pool del registro si es el mismo cluster, o por una conexion
// efimera a la base de mantenimiento `postgres` de la celda.
func (p *DBProvisioner) adminExec(ctx context.Context, target domain.DBTarget, sql string) error {
	if !p.remote(target) {
		_, err := p.pool.Exec(ctx, sql)
		return err
	}
	conn, err := pgx.Connect(ctx, p.directDSNFn(target.Host, target.Port, "postgres"))
	if err != nil {
		return fmt.Errorf("conectar a la celda %s: %w", target.Host, err)
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, sql)
	return err
}

func (p *DBProvisioner) tenantDSN(target domain.DBTarget) string {
	host, port := target.Host, target.Port
	if !p.remote(target) {
		host, port = "", 0
	}
	return p.directDSNFn(host, port, sanitizeDBName(target.DBName))
}

// adminQueryRow lee una fila del catalogo del cluster del destino, por el mismo camino que
// adminExec.
func (p *DBProvisioner) adminQueryRow(ctx context.Context, target domain.DBTarget, sql string, args []any, dest ...any) error {
	if !p.remote(target) {
		return p.pool.QueryRow(ctx, sql, args...).Scan(dest...)
	}
	conn, err := pgx.Connect(ctx, p.directDSNFn(target.Host, target.Port, "postgres"))
	if err != nil {
		return fmt.Errorf("conectar a la celda %s: %w", target.Host, err)
	}
	defer conn.Close(ctx)
	return conn.QueryRow(ctx, sql, args...).Scan(dest...)
}

const pgDuplicateDatabase = "42P04"

// tenantDBMarker es el comentario con el que el alta marca la base de su empresa. Se lee
// desde cualquier base del cluster y distingue la base que creo un intento anterior del alta
// (se adopta) de una ajena con el mismo nombre, como la que se conserva de una empresa
// borrada con el mismo slug (no se toca ni se borra). Solo lleva el uuid de la empresa: se
// concatena en un COMMENT, que no admite parametros.
func tenantDBMarker(tenantID uuid.UUID) string {
	return "core-force-mail tenant " + tenantID.String()
}

// databaseMarker devuelve el comentario de la base y si existe.
func (p *DBProvisioner) databaseMarker(ctx context.Context, target domain.DBTarget, name string) (string, bool, error) {
	var comment *string
	err := p.adminQueryRow(ctx, target,
		`SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = $1`, []any{name}, &comment)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("leer la marca de la base: %w", err)
	}
	if comment == nil {
		return "", true, nil
	}
	return *comment, true, nil
}

// CreateDatabase crea la base de la empresa, la marca como suya y la cierra a PUBLIC:
// Postgres da CONNECT a todo rol de login en cada base nueva, y un rol de celda
// (ops/db/cell-service-role.sh) podria abrirla. Entran el dueno (la credencial de
// plataforma) y los superusuarios. Una base que no se pudo marcar o cerrar se retira:
// abierta seria peor que no creada.
//
// Es idempotente para el alta que la creo: si la base ya existe con su marca (un intento
// anterior se corto), la adopta y repite marca y cierre. Una base con ese nombre sin su marca
// es ajena y responde ErrDatabaseOccupied. Queda una ventana: si el proceso muere entre el
// CREATE y la marca, la base queda sin marca y el reintento la trata como ajena; el fallo es
// seguro (no adopta ni borra) y la base se retira a mano.
func (p *DBProvisioner) CreateDatabase(ctx context.Context, target domain.DBTarget, tenantID uuid.UUID) error {
	name := sanitizeDBName(target.DBName)
	if name == "" {
		return fmt.Errorf("nombre de base vacío para la empresa %s", tenantID)
	}
	marker := tenantDBMarker(tenantID)
	err := p.adminExec(ctx, target, "CREATE DATABASE "+name)
	created := err == nil
	if !created {
		if pgErrorCode(err) != pgDuplicateDatabase {
			return err
		}
		owner, exists, markErr := p.databaseMarker(ctx, target, name)
		if markErr != nil {
			return markErr
		}
		if !exists || owner != marker {
			return domain.ErrDatabaseOccupied
		}
	}
	if err := p.adminExec(ctx, target, fmt.Sprintf("COMMENT ON DATABASE %s IS '%s'", name, marker)); err != nil {
		if created {
			_ = p.adminExec(ctx, target, "DROP DATABASE IF EXISTS "+name)
		}
		return fmt.Errorf("marcar la base: %w", err)
	}
	if err := p.adminExec(ctx, target, fmt.Sprintf("REVOKE CONNECT, TEMPORARY ON DATABASE %s FROM PUBLIC", name)); err != nil {
		_ = p.DropOwnedDatabase(ctx, target, tenantID)
		return fmt.Errorf("cerrar la base a PUBLIC: %w", err)
	}
	return nil
}

// DropOwnedDatabase borra la base solo si lleva la marca de la empresa: deshacer un alta
// nunca borra una base que no creo. WITH (FORCE) cierra las conexiones que quedaran abiertas
// (un pool de la empresa) para que no impidan deshacerla.
func (p *DBProvisioner) DropOwnedDatabase(ctx context.Context, target domain.DBTarget, tenantID uuid.UUID) error {
	name := sanitizeDBName(target.DBName)
	owner, exists, err := p.databaseMarker(ctx, target, name)
	if err != nil {
		return err
	}
	if !exists || owner != tenantDBMarker(tenantID) {
		return nil
	}
	return p.adminExec(ctx, target, fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name))
}

// RunMigrations aplica las migraciones canonicas pendientes sobre la base del tenant.
// Es idempotente (tracking en public.schema_migrations), esta serializado por un advisory
// lock para tolerar varias replicas, y hace baseline de bases preexistentes sin tracking
// antes de aplicar nada. Devuelve ErrMigrationsLocked si otra instancia va primero.
func (p *DBProvisioner) RunMigrations(ctx context.Context, target domain.DBTarget) error {
	tenantPool, err := pgxpool.New(ctx, p.tenantDSN(target))
	if err != nil {
		return fmt.Errorf("conectar a la base del tenant: %w", err)
	}
	defer tenantPool.Close()

	// El lock vive en UNA conexion: adquirirlo y liberarlo desde el pool podria caer en
	// conexiones distintas y no soltarlo nunca.
	conn, err := tenantPool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("adquirir conexion del tenant: %w", err)
	}
	defer conn.Release()

	var acquired bool
	if err := conn.QueryRow(ctx,
		`SELECT pg_try_advisory_lock($1)`, tenantMigrationLockKey,
	).Scan(&acquired); err != nil {
		return fmt.Errorf("advisory lock: %w", err)
	}
	if !acquired {
		return domain.ErrMigrationsLocked
	}
	defer func() {
		// context.Background: el unlock debe ocurrir aunque el ctx original haya expirado.
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, tenantMigrationLockKey)
	}()

	if _, err = conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.schema_migrations (
		name       TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("crear schema_migrations: %w", err)
	}

	if err := p.baselineExistingTenant(ctx, conn); err != nil {
		return err
	}

	for _, m := range p.migrations {
		var exists bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS(
				SELECT 1 FROM public.schema_migrations
				WHERE name = $1 OR name = $2
			)`,
			m.Name, filepath.Base(m.Name),
		).Scan(&exists); err != nil {
			return fmt.Errorf("comprobar migracion %s: %w", m.Name, err)
		}
		if exists {
			continue
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("abrir transaccion para %s: %w", m.Name, err)
		}
		if _, err = tx.Exec(ctx, m.SQL); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migracion %s fallo: %w", m.Name, err)
		}
		if _, err = tx.Exec(ctx,
			`INSERT INTO public.schema_migrations (name) VALUES ($1)`, m.Name,
		); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("registrar migracion %s: %w", m.Name, err)
		}
		if err = tx.Commit(ctx); err != nil {
			return fmt.Errorf("confirmar migracion %s: %w", m.Name, err)
		}
	}
	return nil
}

// baselineExistingTenant marca como aplicadas las migraciones sobre una base que YA tiene
// esquema pero no tiene tracking (restaurada de un respaldo o creada fuera del runner).
// Sin esto, el runner reintentaria migraciones que no toleran repetirse y abortaria al
// tenant en la primera sin llegar nunca a las nuevas.
//
// Contrapartida asumida: si esa base fue creada con una version anterior, las migraciones
// posteriores tambien quedan marcadas y NO se aplican. Por eso se deja el marcador
// baselineMarker, que el endpoint de estado reporta para que se verifique a mano.
func (p *DBProvisioner) baselineExistingTenant(ctx context.Context, conn *pgxpool.Conn) error {
	var tracked int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM public.schema_migrations`).Scan(&tracked); err != nil {
		return fmt.Errorf("contar schema_migrations: %w", err)
	}
	if tracked > 0 {
		return nil
	}

	// Una base recien creada solo tiene el esquema public con la tabla de control. Si
	// ya hay esquemas de servicio, la base se inicializo fuera del runner.
	var initialized bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema NOT IN ('pg_catalog', 'information_schema', 'public')
		)`,
	).Scan(&initialized); err != nil {
		return fmt.Errorf("sondear esquemas del tenant: %w", err)
	}
	if !initialized {
		return nil
	}

	batch := make([]string, 0, len(p.migrations)+1)
	for _, m := range p.migrations {
		batch = append(batch, m.Name)
	}
	batch = append(batch, baselineMarker)
	if _, err := conn.Exec(ctx,
		`INSERT INTO public.schema_migrations (name)
		 SELECT unnest($1::text[]) ON CONFLICT DO NOTHING`, batch,
	); err != nil {
		return fmt.Errorf("baseline del tenant: %w", err)
	}
	return nil
}

// MigrationStatus reporta el estado de migraciones canonicas de una base de tenant sin
// aplicar nada. Alimenta el endpoint del plano de control.
func (p *DBProvisioner) MigrationStatus(ctx context.Context, target domain.DBTarget) (domain.TenantMigrationStatus, error) {
	var st domain.TenantMigrationStatus

	tenantPool, err := pgxpool.New(ctx, p.tenantDSN(target))
	if err != nil {
		return st, fmt.Errorf("conectar a la base del tenant: %w", err)
	}
	defer tenantPool.Close()

	var tracking bool
	if err := tenantPool.QueryRow(ctx,
		`SELECT to_regclass('public.schema_migrations') IS NOT NULL`,
	).Scan(&tracking); err != nil {
		return st, fmt.Errorf("sondear schema_migrations: %w", err)
	}
	if !tracking {
		for _, m := range p.migrations {
			st.Pending = append(st.Pending, m.Name)
		}
		return st, nil
	}

	rows, err := tenantPool.Query(ctx, `SELECT name FROM public.schema_migrations`)
	if err != nil {
		return st, fmt.Errorf("listar schema_migrations: %w", err)
	}
	defer rows.Close()
	applied := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return st, fmt.Errorf("leer schema_migrations: %w", err)
		}
		applied[name] = true
	}
	if err := rows.Err(); err != nil {
		return st, fmt.Errorf("recorrer schema_migrations: %w", err)
	}

	st.Baselined = applied[baselineMarker]
	for _, m := range p.migrations {
		if applied[m.Name] || applied[filepath.Base(m.Name)] {
			st.Applied++
			continue
		}
		st.Pending = append(st.Pending, m.Name)
	}
	return st, nil
}

// sanitizeDBName deja solo lo que un identificador de base sin comillas admite: el
// nombre se concatena en un CREATE DATABASE y no puede pasarse como parametro.
func sanitizeDBName(s string) string {
	result := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
			result = append(result, c)
		}
	}
	return string(result)
}
