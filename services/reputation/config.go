package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/shopspring/decimal"
)

const (
	defaultPort = 8054
	day         = 24 * time.Hour
)

type settings struct {
	port       int
	policy     domain.Policy
	billingURL string
}

// loadSettings lee y valida la configuracion. Los umbrales, la ventana y los limites son
// decisiones de negocio: no tienen valor por defecto en el codigo, y sin ellos o con uno
// incoherente el servicio no arranca.
func loadSettings() (settings, error) {
	var p envParser
	var st settings
	var err error
	st.port, err = config.EnvInt("REPUTATION_PORT", defaultPort, 1, config.MaxPort)
	p.add(err)
	st.policy = domain.Policy{
		WindowDays: p.windowDays("REPUTATION_WINDOW"),
		Thresholds: domain.Thresholds{
			BounceWarn:     p.fraction("REPUTATION_BOUNCE_WARN"),
			BounceBlock:    p.fraction("REPUTATION_BOUNCE_BLOCK"),
			ComplaintWarn:  p.fraction("REPUTATION_COMPLAINT_WARN"),
			ComplaintBlock: p.fraction("REPUTATION_COMPLAINT_BLOCK"),
			MinVolume:      p.count("REPUTATION_MIN_VOLUME"),
		},
		Defaults: map[domain.Class]domain.Limits{
			domain.ClassTransactional: {
				Hourly: p.count("REPUTATION_DEFAULT_HOURLY_TRANSACTIONAL"),
				Daily:  p.count("REPUTATION_DEFAULT_DAILY_TRANSACTIONAL"),
			},
			domain.ClassMarketing: {
				Hourly: p.count("REPUTATION_DEFAULT_HOURLY_MARKETING"),
				Daily:  p.count("REPUTATION_DEFAULT_DAILY_MARKETING"),
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

// envParser acumula todos los errores de configuracion para informarlos de una vez. Las
// variables de la politica son obligatorias: se exige su presencia y despues se leen con
// pkg/config, cuyo valor por defecto (el minimo del rango) nunca llega a usarse.
type envParser struct {
	errs []error
}

func (p *envParser) add(err error) {
	if err != nil {
		p.errs = append(p.errs, err)
	}
}

func (p *envParser) fail(key, msg string) {
	p.errs = append(p.errs, fmt.Errorf("%s %s", key, msg))
}

func (p *envParser) required(key string) (string, bool) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		p.fail(key, "es obligatoria")
		return "", false
	}
	return v, true
}

// fraction lee un umbral como fraccion de los envios de la ventana, sin NaN ni infinito.
// Que quede entre 0 y 1, ambos excluidos, y el aviso por debajo del bloqueo lo exige
// Policy.Validate.
func (p *envParser) fraction(key string) decimal.Decimal {
	if _, ok := p.required(key); !ok {
		return decimal.Zero
	}
	v, err := config.EnvFloat(key, 0, 0, 1)
	if err != nil {
		p.add(err)
		return decimal.Zero
	}
	return decimal.NewFromFloat(v)
}

// count lee un numero de envios (el volumen minimo o un limite por defecto), acotado como
// cualquier limite de la politica por domain.MaxLimit.
func (p *envParser) count(key string) int64 {
	if _, ok := p.required(key); !ok {
		return 0
	}
	n, err := config.EnvInt(key, 1, 1, int(domain.MaxLimit))
	if err != nil {
		p.add(err)
		return 0
	}
	return int64(n)
}

// windowDays lee la ventana como duracion de Go, de 1 a domain.MaxWindowDays dias, y exige
// dias completos: la ventana se suma por dias naturales UTC.
func (p *envParser) windowDays(key string) int {
	if _, ok := p.required(key); !ok {
		return 0
	}
	d, err := config.EnvDuration(key, day, day, domain.MaxWindowDays*day)
	if err != nil {
		p.add(err)
		return 0
	}
	if d%day != 0 {
		p.fail(key, "debe ser una duracion en dias completos (por ejemplo 168h)")
		return 0
	}
	return int(d / day)
}

func (p *envParser) serviceURL(key string) string {
	v, err := config.RequiredServiceURL(key)
	p.add(err)
	return v
}
