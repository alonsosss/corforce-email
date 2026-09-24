// mail-files guarda los ficheros grandes que un buzon comparte por enlace desde el webmail
// (docs/adr/0014). Plano de EMPRESA: los metadatos viven en el esquema mail_files de la base de cada
// empresa y el contenido en el almacen de objetos (pkg/objectstore, espacio private/). Cada fichero se
// analiza con ClamAV antes de guardarse. Sirve dos entradas: la API interna del webmail
// (/internal/mail-files, que el gateway no enruta) y el enlace publico firmado con
// MAIL_LINK_SIGNING_KEY (/api/v1/public/files, routes.json), que muestra el fichero y lo entrega
// como adjunto con caducidad y tope de descargas.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/objectstore"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	clamadapter "github.com/alonsosss/corforce-email/services/mail-files/internal/adapters/clamav"
	handler "github.com/alonsosss/corforce-email/services/mail-files/internal/adapters/http"
	storeadapter "github.com/alonsosss/corforce-email/services/mail-files/internal/adapters/objectstore"
	"github.com/alonsosss/corforce-email/services/mail-files/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/mail-files/internal/adapters/spool"
	"github.com/alonsosss/corforce-email/services/mail-files/internal/adapters/tenantdb"
	"github.com/alonsosss/corforce-email/services/mail-files/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-files/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	defaultPort = 8061

	// maxFileBytesCeiling es lo que clamd analiza entero (StreamMaxLength y MaxFileSize de
	// deploy/mail/clamav/clamd.conf, 101 MiB) y lo que admite el borde (EDGE_MAX_BODY_SIZE, 101m) menos
	// el margen de la peticion: un fichero mayor no tendria veredicto y la subida fallaria cerrada.
	maxFileBytesCeiling = 100 << 20

	defaultMaxFileBytes        = maxFileBytesCeiling
	defaultExpiryDays          = 7
	defaultMaxExpiryDays       = 30
	maxExpiryDaysCeiling       = 365
	defaultMaxDownloads        = 20
	defaultMaxDownloadsCeiling = 100
	maxDownloadsCeiling        = 10000
	defaultMailboxQuotaBytes   = 2 << 30
	defaultTenantQuotaBytes    = 20 << 30
	maxQuotaBytesCeiling       = 1 << 40
	defaultMaxActivePerMailbox = 200
	defaultMaxConcurrent       = 4
	defaultListLimit           = 200

	defaultUploadTimeout    = 10 * time.Minute
	defaultDownloadTimeout  = 30 * time.Minute
	defaultClamdTimeout     = 3 * time.Minute
	defaultSweepInterval    = 15 * time.Minute
	defaultPendingGrace     = time.Hour
	defaultHistoryRetention = 30 * 24 * time.Hour
	defaultSweepBatch       = 200
	operationTimeout        = 20 * time.Second

	defaultSpoolDir        = "/var/spool/mail-files"
	defaultInternalPerMin  = 600
	defaultPublicPerMinute = 60

	// sweepLockKey es el cerrojo de lider del barrido ("mfls").
	sweepLockKey int64 = 0x6d666c73
)

type settings struct {
	port            int
	linkKey         string
	publicBase      string
	clamdAddr       string
	clamdTimeout    time.Duration
	spoolDir        string
	uploadTimeout   time.Duration
	downloadTimeout time.Duration
	internalPerMin  int
	publicPerMin    int
	app             app.Config
}

func loadSettings() (settings, error) {
	var st settings
	var err error
	if st.port, err = config.EnvInt("MAIL_FILES_PORT", defaultPort, 1, config.MaxPort); err != nil {
		return st, err
	}
	if _, err = middleware.InternalGatewayToken(); err != nil {
		return st, err
	}
	st.linkKey = os.Getenv("MAIL_LINK_SIGNING_KEY")
	if len(st.linkKey) < domain.MinLinkKeyBytes {
		return st, errors.New("MAIL_LINK_SIGNING_KEY debe llegar del almacen de secretos con al menos 32 caracteres")
	}
	if st.publicBase = strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL")); st.publicBase == "" {
		return st, errors.New("PUBLIC_BASE_URL es obligatoria: de ella cuelgan los enlaces de descarga")
	}
	if st.clamdAddr = strings.TrimSpace(os.Getenv("MAIL_FILES_CLAMD_ADDR")); st.clamdAddr == "" {
		return st, errors.New("MAIL_FILES_CLAMD_ADDR es obligatorio: ClamAV analiza cada fichero antes de guardarlo")
	}
	if st.clamdTimeout, err = config.EnvDuration("MAIL_FILES_CLAMD_TIMEOUT", defaultClamdTimeout, 10*time.Second, 15*time.Minute); err != nil {
		return st, err
	}
	st.spoolDir = envString("MAIL_FILES_SPOOL_DIR", defaultSpoolDir)
	if st.uploadTimeout, err = config.EnvDuration("MAIL_FILES_UPLOAD_TIMEOUT", defaultUploadTimeout, time.Minute, time.Hour); err != nil {
		return st, err
	}
	if st.downloadTimeout, err = config.EnvDuration("MAIL_FILES_DOWNLOAD_TIMEOUT", defaultDownloadTimeout, time.Minute, 6*time.Hour); err != nil {
		return st, err
	}
	if st.internalPerMin, err = config.EnvInt("MAIL_FILES_RATE_LIMIT_PER_MIN", defaultInternalPerMin, 1, 600000); err != nil {
		return st, err
	}
	if st.publicPerMin, err = config.EnvInt("MAIL_FILES_PUBLIC_RATE_LIMIT_PER_MIN", defaultPublicPerMinute, 1, 60000); err != nil {
		return st, err
	}
	if st.app, err = appConfig(st.downloadTimeout); err != nil {
		return st, err
	}
	return st, nil
}

// appConfig lee la politica y el barrido. La gracia de borrado nunca es menor que el plazo de una
// descarga: un objeto no se borra mientras una descarga que empezo a tiempo puede seguir en curso.
func appConfig(downloadTimeout time.Duration) (app.Config, error) {
	var c app.Config
	var err error
	p := &c.Policy
	maxFile, err := config.EnvInt("MAIL_FILES_MAX_FILE_BYTES", defaultMaxFileBytes, 1<<20, maxFileBytesCeiling)
	if err != nil {
		return c, err
	}
	p.MaxFileBytes = int64(maxFile)
	if p.MaxExpiryDays, err = config.EnvInt("MAIL_FILES_MAX_EXPIRY_DAYS", defaultMaxExpiryDays, 1, maxExpiryDaysCeiling); err != nil {
		return c, err
	}
	if p.DefaultExpiryDays, err = config.EnvInt("MAIL_FILES_DEFAULT_EXPIRY_DAYS", min(defaultExpiryDays, p.MaxExpiryDays), 1, p.MaxExpiryDays); err != nil {
		return c, err
	}
	if p.MaxDownloads, err = config.EnvInt("MAIL_FILES_MAX_DOWNLOADS", defaultMaxDownloadsCeiling, 1, maxDownloadsCeiling); err != nil {
		return c, err
	}
	if p.DefaultMaxDownloads, err = config.EnvInt("MAIL_FILES_DEFAULT_MAX_DOWNLOADS", min(defaultMaxDownloads, p.MaxDownloads), 1, p.MaxDownloads); err != nil {
		return c, err
	}
	mailboxQuota, err := config.EnvInt("MAIL_FILES_MAILBOX_QUOTA_BYTES", defaultMailboxQuotaBytes, 1<<20, maxQuotaBytesCeiling)
	if err != nil {
		return c, err
	}
	tenantQuota, err := config.EnvInt("MAIL_FILES_TENANT_QUOTA_BYTES", defaultTenantQuotaBytes, 1<<20, maxQuotaBytesCeiling)
	if err != nil {
		return c, err
	}
	p.MailboxQuotaBytes, p.TenantQuotaBytes = int64(mailboxQuota), int64(tenantQuota)
	if p.MaxActivePerMailbox, err = config.EnvInt("MAIL_FILES_MAX_ACTIVE_PER_MAILBOX", defaultMaxActivePerMailbox, 1, 100000); err != nil {
		return c, err
	}
	if err := p.Validate(); err != nil {
		return c, fmt.Errorf("MAIL_FILES_*: %w", err)
	}
	if c.MaxConcurrentUploads, err = config.EnvInt("MAIL_FILES_MAX_CONCURRENT_UPLOADS", defaultMaxConcurrent, 1, 64); err != nil {
		return c, err
	}
	c.ListLimit = defaultListLimit
	if c.SweepInterval, err = config.EnvDuration("MAIL_FILES_SWEEP_INTERVAL", defaultSweepInterval, 0, 24*time.Hour); err != nil {
		return c, err
	}
	if c.SweepInterval > 0 && c.SweepInterval < time.Minute {
		return c, errors.New("MAIL_FILES_SWEEP_INTERVAL debe ser 0 (desactivado) o al menos 1m")
	}
	if c.PendingGrace, err = config.EnvDuration("MAIL_FILES_PENDING_GRACE", defaultPendingGrace, 10*time.Minute, 24*time.Hour); err != nil {
		return c, err
	}
	if c.HistoryRetention, err = config.EnvDuration("MAIL_FILES_HISTORY_RETENTION", defaultHistoryRetention, time.Hour, 365*24*time.Hour); err != nil {
		return c, err
	}
	c.DeleteGrace = downloadTimeout
	c.SweepBatch = defaultSweepBatch
	return c, nil
}

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	response.SetUnexpectedLogger(logger)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	st, err := loadSettings()
	if err != nil {
		log.Fatalf("mail-files: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registryPool, err := db.NewPool(ctx, cfg.Postgres.DSN(), logger)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer registryPool.Close()
	tenantDB, mgr := db.NewTenantRouting(registryPool.Pool, cfg.Postgres, logger)
	defer mgr.CloseAll()

	links, err := domain.NewLinkSigner(st.linkKey, st.publicBase)
	if err != nil {
		log.Fatalf("mail-files: %v", err)
	}
	spoolDir, err := spool.New(st.spoolDir, st.uploadTimeout)
	if err != nil {
		log.Fatalf("mail-files: MAIL_FILES_SPOOL_DIR: %v", err)
	}

	// Sin almacen el servicio arranca sin la funcion (la interfaz no la ofrece) en vez de caer en bucle.
	var store *storeadapter.Store
	objects, err := objectstore.FromEnv()
	switch {
	case err != nil:
		log.Fatalf("mail-files: almacen de objetos: %v", err)
	case objects == nil:
		logger.Warn("mail-files: MINIO_ENDPOINT sin definir; el envio de ficheros grandes queda desactivado")
	default:
		vctx, vcancel := context.WithTimeout(ctx, 10*time.Second)
		if verr := objects.Verify(vctx); verr != nil {
			logger.Warn("mail-files: el bucket no responde al arrancar; se reintentara en cada operacion", zap.Error(verr))
		}
		vcancel()
		store = storeadapter.New(objects)
	}

	deps := app.Deps{
		Repo:    postgres.NewRepository(&db.ContextPool{}),
		Binder:  tenantdb.NewBinder(tenantDB),
		Tenants: tenantdb.NewTenants(tenantDB),
		Leader: func(c context.Context) (func(), bool) {
			return db.TryLeaderLock(c, registryPool.Pool, sweepLockKey)
		},
		Scanner: clamadapter.New(st.clamdAddr, st.clamdTimeout),
		Spool:   spoolDir,
		Links:   links,
		Config:  st.app,
		Logger:  logger,
	}
	if store != nil {
		deps.Store = store
	}
	uc, err := app.New(deps)
	if err != nil {
		log.Fatalf("mail-files: %v", err)
	}
	go uc.RunSweeper(ctx)

	h, err := handler.NewHandler(uc, handler.Config{
		UploadTimeout: st.uploadTimeout, DownloadTimeout: st.downloadTimeout, OperationTimeout: operationTimeout,
	}, logger)
	if err != nil {
		log.Fatalf("mail-files: %v", err)
	}

	srv := server.New(st.port, router(h, st, logger), logger)
	runErr := srv.Run()
	cancel()
	if runErr != nil {
		logger.Fatal("server error", zap.Error(runErr))
	}
}

// router es la cadena de la peticion. Unica entrada: el gateway. La API interna solo la llaman
// servicios de la plataforma (RequireInternalCaller) con cupo por buzon, porque todo llega desde las
// pocas IP del webmail; el enlace publico lleva cupo por IP, ademas del general del gateway.
func router(h *handler.Handler, st settings, logger *zap.Logger) http.Handler {
	internal := chain(h.InternalRoutes(),
		middleware.InjectFromGateway,
		middleware.RequireInternalCaller,
		limitPerMailbox(middleware.NewRateLimiter(st.internalPerMin, time.Minute)),
	)
	public := chain(h.PublicRoutes(), middleware.NewRateLimiter(st.publicPerMin, time.Minute).Limit)
	split := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == handler.InternalPrefix || strings.HasPrefix(r.URL.Path, handler.InternalPrefix+"/"):
			internal.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, domain.DownloadPath+"/"):
			public.ServeHTTP(w, r)
		default:
			response.ErrNotFound(w, "ruta desconocida")
		}
	})
	return chain(split, middleware.RequestID, middleware.RequireGatewayToken, middleware.SecureHeaders, middleware.Logger(logger))
}

func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// limitPerMailbox es el cupo de la API interna por buzon; una cabecera que no es un UUID cuenta en un
// cupo comun y la rechaza despues el manejador.
func limitPerMailbox(rl *middleware.RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := "mailbox:invalid"
			if id, err := uuid.Parse(strings.TrimSpace(r.Header.Get(handler.MailboxHeader))); err == nil {
				key = "mailbox:" + id.String()
			}
			if ok, reset := rl.AllowKey(r.Context(), key); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(reset.Seconds())))))
				response.Err(w, http.StatusTooManyRequests, "RATE_LIMITED", "demasiadas peticiones del buzon")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func envString(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
