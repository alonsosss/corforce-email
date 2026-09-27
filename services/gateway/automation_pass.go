package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/prometheus/client_golang/prometheus"
)

// Pase de desarrollo del detector de navegador automatizado (docs/Plan_Proteccion_Frente_a_Bots.md,
// capa 2). La web compila en enforce y en produccion no hay interruptor en la interfaz ni endpoint que
// lo cambie; lo unico que puede abrir el paso es este pase, que vive en el entorno del gateway:
// mientras dura (WEB_AUTOMATION_OBSERVE_UNTIL) y solo para las IP de WEB_AUTOMATION_OBSERVE_CIDRS,
// el documento de la aplicacion sale con una etiqueta <meta> que la web lee como modo observe:
// detecta, comunica la referencia y monta la aplicacion igual. Para el resto de internet no cambia
// nada. El pase caduca solo, no puede durar mas de una semana ni abarcar mas de 256 direcciones, y
// mientras esta abierto lo dice una metrica con su alerta: no se puede dejar encendido sin querer.
// Se enciende y se apaga con scripts/pase-automatizacion.sh.

const (
	automationPassMeta    = `<meta name="cfm-automation-mode" content="observe">`
	maxAutomationPass     = 7 * 24 * time.Hour
	maxAutomationPassHost = 8 // bits de host: un /24 en IPv4, un /120 en IPv6
)

// registerAutomationPassGauge publica el estado del pase; se evalua en cada scrape, asi que caducar
// se ve sin reiniciar. Se registra desde main, con el pase ya cargado (o sin el: entonces vale 0).
func registerAutomationPassGauge(p *automationPass) {
	prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "gateway_automation_observe_pass",
		Help: "1 mientras el pase de desarrollo del detector de navegador automatizado esta abierto (WEB_AUTOMATION_OBSERVE_UNTIL en el futuro), 0 si no.",
	}, func() float64 {
		if p.open() {
			return 1
		}
		return 0
	}))
}

type automationPass struct {
	until time.Time
	cidrs []*net.IPNet
	now   func() time.Time
}

// loadAutomationPass lee el pase del entorno. Sin WEB_AUTOMATION_OBSERVE_UNTIL no hay pase (nil);
// con ella, todo lo demas tiene que estar bien o el gateway no arranca: un pase mal escrito no se
// interpreta con buena voluntad.
func loadAutomationPass(now func() time.Time) (*automationPass, error) {
	raw := strings.TrimSpace(os.Getenv("WEB_AUTOMATION_OBSERVE_UNTIL"))
	if raw == "" {
		return nil, nil
	}
	until, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("WEB_AUTOMATION_OBSERVE_UNTIL must be RFC3339 (2026-09-27T18:00:00Z): %q", raw)
	}
	if until.Sub(now()) > maxAutomationPass {
		return nil, fmt.Errorf("WEB_AUTOMATION_OBSERVE_UNTIL must be within 7 days: %q", raw)
	}
	spec := strings.TrimSpace(os.Getenv("WEB_AUTOMATION_OBSERVE_CIDRS"))
	if spec == "" {
		return nil, errors.New("WEB_AUTOMATION_OBSERVE_CIDRS is required with WEB_AUTOMATION_OBSERVE_UNTIL")
	}
	var cidrs []*net.IPNet
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		_, cidr, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("WEB_AUTOMATION_OBSERVE_CIDRS: %q is not a CIDR", part)
		}
		if ones, bits := cidr.Mask.Size(); bits-ones > maxAutomationPassHost {
			return nil, fmt.Errorf("WEB_AUTOMATION_OBSERVE_CIDRS: %q covers more than 256 addresses", part)
		}
		cidrs = append(cidrs, cidr)
	}
	if len(cidrs) == 0 {
		return nil, errors.New("WEB_AUTOMATION_OBSERVE_CIDRS has no CIDR")
	}
	return &automationPass{until: until, cidrs: cidrs, now: now}, nil
}

// open: el pase existe y no ha caducado. Caducar no exige reiniciar nada.
func (p *automationPass) open() bool {
	return p != nil && p.now().Before(p.until)
}

func (p *automationPass) allows(ip string) bool {
	if !p.open() {
		return false
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, cidr := range p.cidrs {
		if cidr.Contains(parsed) {
			return true
		}
	}
	return false
}

// decorate anade la etiqueta al documento de la aplicacion cuando la peticion viene de una IP del
// pase; en cualquier otro caso devuelve el cuerpo tal cual.
func (p *automationPass) decorate(req *http.Request, body []byte) []byte {
	if !p.allows(middleware.GetClientIP(req.Context())) {
		return body
	}
	i := bytes.Index(body, []byte("</head>"))
	if i < 0 {
		return body
	}
	out := make([]byte, 0, len(body)+len(automationPassMeta))
	out = append(out, body[:i]...)
	out = append(out, automationPassMeta...)
	return append(out, body[i:]...)
}
