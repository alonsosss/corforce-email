package main

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/shopspring/decimal"
)

const defaultPort = 8054

type settings struct {
	port       int
	policy     domain.Policy
	billingURL string
}

// loadSettings lee y valida la configuracion. Los umbrales, la ventana y los limites son
// decisiones de negocio: no tienen valor por defecto en el codigo, y sin ellos o con uno
// incoherente el servicio no arranca.
func loadSettings(getenv func(string) string) (settings, error) {
	p := envParser{getenv: getenv}
	st := settings{port: defaultPort}
	if v := strings.TrimSpace(getenv("REPUTATION_PORT")); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil || port < 1 || port > 65535 {
			p.fail("REPUTATION_PORT", "debe ser un puerto valido")
		} else {
			st.port = port
		}
	}
	st.policy = domain.Policy{
		WindowDays: p.windowDays("REPUTATION_WINDOW"),
		Thresholds: domain.Thresholds{
			BounceWarn:     p.fraction("REPUTATION_BOUNCE_WARN"),
			BounceBlock:    p.fraction("REPUTATION_BOUNCE_BLOCK"),
			ComplaintWarn:  p.fraction("REPUTATION_COMPLAINT_WARN"),
			ComplaintBlock: p.fraction("REPUTATION_COMPLAINT_BLOCK"),
			MinVolume:      p.positiveInt("REPUTATION_MIN_VOLUME"),
		},
		Defaults: map[domain.Class]domain.Limits{
			domain.ClassTransactional: {
				Hourly: p.positiveInt("REPUTATION_DEFAULT_HOURLY_TRANSACTIONAL"),
				Daily:  p.positiveInt("REPUTATION_DEFAULT_DAILY_TRANSACTIONAL"),
			},
			domain.ClassMarketing: {
				Hourly: p.positiveInt("REPUTATION_DEFAULT_HOURLY_MARKETING"),
				Daily:  p.positiveInt("REPUTATION_DEFAULT_DAILY_MARKETING"),
			},
		},
	}
	st.billingURL = p.serviceURL("BILLING_URL")
	// La coherencia entre valores solo se juzga cuando todos se pudieron leer: con uno
	// ausente, el error de coherencia seria ruido sobre el verdadero.
	if len(p.errs) == 0 {
		if err := st.policy.Validate(); err != nil {
			p.errs = append(p.errs, err)
		}
	}
	return st, errors.Join(p.errs...)
}

// envParser acumula todos los errores de configuracion para informarlos de una vez.
type envParser struct {
	getenv func(string) string
	errs   []error
}

func (p *envParser) fail(key, msg string) {
	p.errs = append(p.errs, fmt.Errorf("%s %s", key, msg))
}

func (p *envParser) required(key string) (string, bool) {
	v := strings.TrimSpace(p.getenv(key))
	if v == "" {
		p.fail(key, "es obligatoria")
		return "", false
	}
	return v, true
}

func (p *envParser) fraction(key string) decimal.Decimal {
	v, ok := p.required(key)
	if !ok {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(v)
	if err != nil {
		p.fail(key, "debe ser una fraccion decimal (por ejemplo 0.02)")
		return decimal.Zero
	}
	return d
}

func (p *envParser) positiveInt(key string) int64 {
	v, ok := p.required(key)
	if !ok {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 1 {
		p.fail(key, "debe ser un entero mayor que 0")
		return 0
	}
	return n
}

// windowDays lee la ventana como duracion de Go y exige dias completos: la ventana se
// suma por dias naturales UTC.
func (p *envParser) windowDays(key string) int {
	v, ok := p.required(key)
	if !ok {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 || d%(24*time.Hour) != 0 {
		p.fail(key, "debe ser una duracion en dias completos (por ejemplo 168h)")
		return 0
	}
	return int(d / (24 * time.Hour))
}

func (p *envParser) serviceURL(key string) string {
	v, ok := p.required(key)
	if !ok {
		return ""
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		p.fail(key, "debe ser una URL http(s) absoluta")
		return ""
	}
	return strings.TrimRight(v, "/")
}
