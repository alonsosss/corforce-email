package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	"github.com/alonsosss/corforce-email/services/organization/internal/adapters/accesscontrolcli"
	handler "github.com/alonsosss/corforce-email/services/organization/internal/adapters/http"
	"github.com/alonsosss/corforce-email/services/organization/internal/adapters/identitycli"
	natsadapter "github.com/alonsosss/corforce-email/services/organization/internal/adapters/nats"
	"github.com/alonsosss/corforce-email/services/organization/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/organization/internal/app"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// Rutas por defecto de las migraciones dentro de la imagen (ver Dockerfile).
const (
	defaultTenantMigrationDir   = "/app/migrations/tenant"
	defaultRegistryMigrationDir = "/app/migrations/registry"
	defaultPort                 = 8003
)

func loadMigrations(dir string) []postgres.MigrationFile {
	var migrations []postgres.MigrationFile
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			rel = entry.Name()
		}
		migrations = append(migrations, postgres.MigrationFile{
			Name: filepath.ToSlash(rel),
			SQL:  string(data),
		})
		return nil
	})
	if err != nil {
		return nil
	}
	sort.Slice(migrations, func(i, j int) bool {
		ai, aj := migrationOrder(migrations[i].Name), migrationOrder(migrations[j].Name)
		if ai != aj {
			return ai < aj
		}
		return migrations[i].Name < migrations[j].Name
	})
	return migrations
}

var migrationPrefix = regexp.MustCompile(`(^|/)(\d+)_`)

// migrationOrder extrae el numero global del nombre; sin numero, la migracion va al
// final para que nunca adelante a una numerada.
func migrationOrder(name string) int {
	match := migrationPrefix.FindStringSubmatch(name)
	if len(match) < 3 {
		return 999999
	}
	value, err := strconv.Atoi(match[2])
	if err != nil {
		return 999999
	}
	return value
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envDuration lee una duracion (5m, 30s). Un valor que no se entiende se avisa y toma el
// de por defecto: arrancar con un cero dejaria la saga sin arriendo.
func envDuration(key string, fallback time.Duration, logger *zap.Logger) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		logger.Warn("duracion no valida; se usa la de por defecto",
			zap.String("variable", key), zap.String("valor", raw), zap.Duration("por_defecto", fallback))
		return fallback
	}
	return d
}

// serviceURL resuelve la direccion interna de otro servicio: <SERVICIO>_URL si esta, o
// <SERVICIO>_HOST y <SERVICIO>_HOST_PORT, las mismas que el gateway lee de routes.json, con
// su nombre y puerto de compose por defecto.
func serviceURL(prefix, defaultHost, defaultPort string) string {
	if u := os.Getenv(prefix + "_URL"); u != "" {
		return u
	}
	return "http://" + envOrDefault(prefix+"_HOST", defaultHost) + ":" + envOrDefault(prefix+"_HOST_PORT", defaultPort)
}

// waitReady espera a que el servicio responda en /healthz. Va por un cliente aparte a
// proposito: los fallos de arranque, contados por el cliente que comparte la saga, abririan
// su cortacircuitos y un alta pedida en esos segundos fallaria sin llegar a intentarse.
func waitReady(ctx context.Context, baseURL string) error {
	probe := &http.Client{Timeout: 2 * time.Second}
	url := strings.TrimRight(baseURL, "/") + "/healthz"
	wait := time.Second
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		if resp, err := probe.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		if wait < 10*time.Second {
			wait *= 2
		}
	}
}

// reseedSystemRoles pide la resiembra hasta que access-control la aplique: al arrancar la
// plataforma, organization migra el registro antes de que access-control este arriba.
func reseedSystemRoles(ctx context.Context, uc *app.OrganizationUseCase, logger *zap.Logger) {
	wait := 2 * time.Second
	for {
		roles, err := uc.ReseedAllRoles(ctx)
		if err == nil {
			logger.Info("catalogo de permisos aplicado al rol del sistema de las empresas", zap.Int("roles", roles))
			return
		}
		logger.Warn("resiembra del rol del sistema pendiente; se reintenta", zap.Duration("espera", wait), zap.Error(err))
		select {
		case <-ctx.Done():
			logger.Error("resiembra del rol del sistema sin aplicar", zap.Error(ctx.Err()))
			return
		case <-time.After(wait):
		}
		if wait < 30*time.Second {
			wait *= 2
		}
	}
}

// recoverSagas retoma cada intervalo las sagas de empresa sin dueno. Varias replicas pueden
// hacerlo a la vez: cada saga la toma una sola por su arriendo. La primera pasada espera un
// intervalo: al arrancar la plataforma, access-control e identity aun no responden.
func recoverSagas(uc *app.OrganizationUseCase, interval time.Duration, logger *zap.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		if n, err := uc.RecoverSagas(context.Background()); err != nil {
			logger.Warn("barrido de sagas de empresa fallido", zap.Error(err))
		} else if n > 0 {
			logger.Info("sagas de empresa retomadas", zap.Int("sagas", n))
		}
	}
}

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	response.SetUnexpectedLogger(logger)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx := context.Background()

	pool, err := db.NewPool(ctx, cfg.Postgres.DSN(), logger)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer pool.Close()

	// Migraciones de la base de registro: se aplican en cada arranque de forma
	// idempotente, antes de tocar cualquier tabla propia.
	registryMigrator := postgres.NewRegistryMigrator(pool.Pool,
		loadMigrations(envOrDefault("REGISTRY_MIGRATION_DIR", defaultRegistryMigrationDir)), logger)
	if err := registryMigrator.Run(ctx); err != nil {
		logger.Error("fallo al aplicar migraciones del registro", zap.Error(err))
	}

	// Directo al Postgres real: los advisory locks de RunMigrations exigen semantica
	// de sesion, que pgbouncer en pool_mode=transaction no garantiza (el unlock puede
	// caer en otra conexion backend y dejar el lock pegado, o el try_lock dar exito
	// falso entre replicas).
	provisioner := postgres.NewDBProvisioner(pool.Pool,
		loadMigrations(envOrDefault("TENANT_MIGRATION_DIR", defaultTenantMigrationDir)),
		cfg.Postgres.TenantDirectDSNAt, cfg.Postgres.Host)

	// Los eventos del plano de control son persistentes. Sin NATS el servicio arranca
	// igual: el alta funciona y los avisos no salen, que es preferible a no poder
	// operar la plataforma.
	publisher := natsadapter.NewTenantPublisher(nil)
	if bus, err := events.NewBus(cfg.NATS.URL, logger); err != nil {
		logger.Warn("NATS no disponible: los eventos de tenant no se publicaran", zap.Error(err))
	} else {
		defer bus.Close()
		// Subjects concretos, sin comodin: un stream por hecho evita solapar a
		// cualquier otro stream del dominio.
		if err := bus.EnsureStream(natsadapter.StreamName, []string{
			natsadapter.SubjectTenantCreated,
			natsadapter.SubjectTenantStatusChanged,
			natsadapter.SubjectTenantModulesChanged,
		}); err != nil {
			logger.Warn("ensure stream ORGANIZATION", zap.Error(err))
		}
		publisher = natsadapter.NewTenantPublisher(bus)
	}

	// DEFAULT_CELL_CODE no tiene valor en codigo a proposito: la celda es un dato de
	// despliegue. Sin ella, cada alta debe indicar su celda o recibe 422.
	defaultCellCode := os.Getenv("DEFAULT_CELL_CODE")
	if defaultCellCode == "" {
		logger.Warn("DEFAULT_CELL_CODE no configurado: cada alta de tenant debe indicar cell_code")
	}

	// El rol del sistema, sus asignaciones y las cuentas de cada empresa son de
	// access-control e identity: la saga se los pide por su API interna, con el mismo
	// token interno que el gateway.
	internalToken := os.Getenv("INTERNAL_GATEWAY_TOKEN")
	accessControlURL := serviceURL("ACCESS_CONTROL", "access-control", "8002")
	uc := app.NewOrganizationUseCase(app.Dependencies{
		Tenants:         postgres.NewTenantRepo(pool.Pool),
		Cells:           postgres.NewCellRepo(pool.Pool),
		Sagas:           postgres.NewTenantSagaRepo(pool.Pool),
		Provisioner:     provisioner,
		Access:          accesscontrolcli.New(accessControlURL, internalToken),
		Identity:        identitycli.New(serviceURL("IDENTITY", "identity", "8001"), internalToken),
		Modules:         postgres.NewModulesRepo(pool.Pool),
		Publisher:       publisher,
		DefaultCellCode: defaultCellCode,
		SagaLease:       envDuration("ORGANIZATION_SAGA_LEASE", 5*time.Minute, logger),
		Logger:          logger,
	})

	// Reaplica el catalogo de permisos al rol del sistema de todas las empresas. Va aparte
	// del barrido de migraciones y antes que el, despues de migrar el registro: es lo que
	// hace que un permiso nuevo llegue a las empresas que YA existian sin que nadie tenga
	// que entrar a pulsar nada.
	if os.Getenv("RUN_ROLE_RESEED") != "false" {
		go func() {
			seedCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := waitReady(seedCtx, accessControlURL); err != nil {
				logger.Error("access-control no respondio; resiembra del rol del sistema sin aplicar", zap.Error(err))
				return
			}
			reseedSystemRoles(seedCtx, uc, logger)
		}()
	}

	// Sagas de alta y baja que se quedaron sin dueno (una instancia murio a mitad o un
	// fallo solto el arriendo): se retoman cada intervalo.
	go recoverSagas(uc, envDuration("ORGANIZATION_SAGA_SWEEP_INTERVAL", time.Minute, logger), logger)

	// Aplica migraciones canonicas pendientes a los tenants existentes al arrancar.
	// Corre EN SEGUNDO PLANO para no retrasar el health-check ni el arranque del HTTP, es
	// idempotente y esta serializado por advisory lock (varias replicas no colisionan).
	// Se apaga con RUN_TENANT_MIGRATIONS=false si hiciera falta desplegar sin migrar.
	if os.Getenv("RUN_TENANT_MIGRATIONS") != "false" {
		go func() {
			sweepCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			res, err := uc.MigrateAllTenants(sweepCtx)
			if err != nil {
				logger.Error("fallo el barrido de migraciones de tenants", zap.Error(err))
				return
			}
			logger.Info("barrido de migraciones de tenants completado",
				zap.Int("migrated", res.Migrated), zap.Int("failed", res.Failed),
				zap.Int("locked", res.Locked))
		}()
	}

	h := handler.NewHandler(uc, authz.NewCheckerFromEnv())

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	limiter := middleware.NewRateLimiter(100, time.Minute)
	r.Use(limiter.Limit)
	r.Mount("/", h.Routes())

	port := defaultPort
	if p := os.Getenv("ORGANIZATION_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}

	srv := server.New(port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}
