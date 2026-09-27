package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// Referencias de navegador automatizado (docs/Plan_Proteccion_Frente_a_Bots.md, capa 2). La web
// detecta la automatizacion antes de montarse, muestra una pagina de bloqueo con una referencia y
// la envia aqui. El gateway la cuenta y la escribe en su registro con la IP y el agente: asi un
// bloqueo indebido se busca por su referencia y una rafaga se ve en la alerta. No hay empresa (la
// deteccion corre antes del inicio de sesion), asi que no es un evento de auditoria de nadie.
//
// Es una ruta publica sin efectos: el cuerpo se acota, cada campo se valida contra una lista
// cerrada y nada de lo recibido se devuelve ni se interpreta.

const automationReportMaxBody = 2 << 10

var (
	automationReference = regexp.MustCompile(`^[a-z0-9]{8,32}$`)
	automationPath      = regexp.MustCompile(`^/[A-Za-z0-9._~/-]{0,199}$`)
	// Las senales que emite web/src/security/automation.ts; otra cosa es 422.
	automationSignals = map[string]struct{}{
		"webdriver": {}, "headless_user_agent": {}, "automation_globals": {},
		"zero_window": {}, "no_languages": {}, "no_plugins": {},
	}
)

var (
	automationReports = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gateway_automation_detected_total",
		Help: "Bloqueos de navegador automatizado que la web comunico al gateway (uno por pagina de bloqueo mostrada).",
	})
	automationSignalHits = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_automation_signals_total",
		Help: "Senales de automatizacion presentes en los bloqueos comunicados, por senal (webdriver, headless_user_agent, automation_globals, zero_window, no_languages, no_plugins).",
	}, []string{"signal"})
)

func init() {
	for s := range automationSignals {
		automationSignalHits.WithLabelValues(s)
	}
	prometheus.MustRegister(automationReports, automationSignalHits)
}

type automationReport struct {
	Reference string   `json:"reference"`
	Path      string   `json:"path"`
	Signals   []string `json:"signals"`
}

// automationReportHandler recibe la referencia de un bloqueo: 204 si es valida, 422 si no.
func automationReportHandler(logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, automationReportMaxBody)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		var in automationReport
		if err := dec.Decode(&in); err != nil {
			response.ErrValidation(w, "cuerpo no válido")
			return
		}
		if !automationReference.MatchString(in.Reference) || !automationPath.MatchString(in.Path) ||
			len(in.Signals) == 0 || len(in.Signals) > len(automationSignals) {
			response.ErrValidation(w, "referencia, ruta o señales no válidas")
			return
		}
		seen := make(map[string]struct{}, len(in.Signals))
		for _, s := range in.Signals {
			if _, ok := automationSignals[s]; !ok {
				response.ErrValidation(w, "señal desconocida")
				return
			}
			if _, dup := seen[s]; dup {
				response.ErrValidation(w, "señal repetida")
				return
			}
			seen[s] = struct{}{}
		}
		automationReports.Inc()
		for _, s := range in.Signals {
			automationSignalHits.WithLabelValues(s).Inc()
		}
		ua := r.UserAgent()
		if len(ua) > 200 {
			ua = ua[:200]
		}
		logger.Info("navegador automatizado bloqueado",
			zap.String("reference", in.Reference),
			zap.String("path", in.Path),
			zap.String("signals", strings.Join(in.Signals, ",")),
			zap.String("ip", middleware.GetClientIP(r.Context())),
			zap.String("user_agent", ua))
		w.WriteHeader(http.StatusNoContent)
	}
}
