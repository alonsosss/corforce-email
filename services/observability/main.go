package main

import (
	"log"
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/server"
	handler "github.com/alonsosss/corforce-email/services/observability/internal/adapters/http"
	"github.com/alonsosss/corforce-email/services/observability/internal/adapters/loki"
	promadapter "github.com/alonsosss/corforce-email/services/observability/internal/adapters/prometheus"
	"github.com/alonsosss/corforce-email/services/observability/internal/app"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// Servicio de plataforma sin base de datos ni bus: consulta la pila de observabilidad
// (docker-compose.observability.yml) en nombre del superadmin. Sin LOKI_URL arranca igual y el visor
// responde 503 NOT_CONFIGURED: la pila es opcional y vive en un compose aparte.
const defaultPort = 8059

// settings es la configuracion propia del servicio, leida y validada antes de escuchar.
type settings struct {
	port int
	// lokiURL vacia desactiva el visor de registros.
	lokiURL string
	perms   *authz.Checker
}

// loadSettings falla con un puerto fuera de rango, una LOKI_URL que no es una URL base interna
// (config.ServiceURL) o sin token interno fuera de desarrollo o prueba.
func loadSettings() (settings, error) {
	var st settings
	var err error
	if st.port, err = config.EnvInt("OBSERVABILITY_PORT", defaultPort, 1, config.MaxPort); err != nil {
		return st, err
	}
	if st.lokiURL, err = config.ServiceURL("LOKI_URL", ""); err != nil {
		return st, err
	}
	if st.perms, err = authz.CheckerFromEnv(); err != nil {
		return st, err
	}
	return st, nil
}

// logsUseCase evita el nil tipado: un *loki.Client nil dentro de la interfaz no seria nil y el caso de uso
// intentaria usarlo.
func logsUseCase(st settings, metrics *promadapter.Metrics, logger *zap.Logger) *app.LogsUseCase {
	deps := app.LogsDeps{Logger: logger}
	if metrics != nil {
		deps.Metrics = metrics
	}
	if st.lokiURL == "" {
		logger.Info("visor de registros desactivado: falta LOKI_URL")
	} else {
		deps.Store = loki.New(st.lokiURL)
	}
	return app.NewLogsUseCase(deps)
}

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	response.SetUnexpectedLogger(logger)

	st, err := loadSettings()
	if err != nil {
		log.Fatalf("observability: %v", err)
	}

	uc := logsUseCase(st, promadapter.New(), logger)
	r := apiRouter(handler.NewHandler(uc, st.perms).Routes(), logger)

	srv := server.New(st.port, r, logger)
	if err := srv.Run(); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}

// apiRouter monta el API que llega por el gateway, tras el token interno.
func apiRouter(routes http.Handler, logger *zap.Logger) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Use(middleware.SecureHeaders)
	r.Use(middleware.Logger(logger))
	r.Use(middleware.NewRateLimiter(120, time.Minute).Limit)
	r.Mount("/", routes)
	return r
}
