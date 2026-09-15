package main

import (
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
)

func validEnv() map[string]string {
	return map[string]string{
		"REPUTATION_WINDOW":                       "168h",
		"REPUTATION_MIN_VOLUME":                   "200",
		"REPUTATION_BOUNCE_WARN":                  "0.02",
		"REPUTATION_BOUNCE_BLOCK":                 "0.04",
		"REPUTATION_COMPLAINT_WARN":               "0.0005",
		"REPUTATION_COMPLAINT_BLOCK":              "0.0008",
		"REPUTATION_DEFAULT_HOURLY_TRANSACTIONAL": "2000",
		"REPUTATION_DEFAULT_DAILY_TRANSACTIONAL":  "20000",
		"REPUTATION_DEFAULT_HOURLY_MARKETING":     "5000",
		"REPUTATION_DEFAULT_DAILY_MARKETING":      "50000",
		"BILLING_URL":                             "http://billing:8055/",
	}
}

// setEnv deja en el entorno exactamente env: toda variable de la configuracion que no aparece
// queda vacia, que para reputation es ausente.
func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for k := range validEnv() {
		t.Setenv(k, env[k])
	}
	t.Setenv("REPUTATION_PORT", env["REPUTATION_PORT"])
}

func TestLoadSettingsValida(t *testing.T) {
	setEnv(t, validEnv())
	st, err := loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if st.port != defaultPort || st.billingURL != "http://billing:8055" {
		t.Fatalf("puerto y billing: %+v", st)
	}
	p := st.policy
	if p.WindowDays != 7 || p.Thresholds.MinVolume != 200 || p.Thresholds.ComplaintBlock.String() != "0.0008" {
		t.Fatalf("politica: %+v", p)
	}
	if p.Defaults[domain.ClassMarketing] != (domain.Limits{Hourly: 5000, Daily: 50000}) {
		t.Fatalf("limites: %+v", p.Defaults)
	}

	env := validEnv()
	env["REPUTATION_PORT"] = "18054"
	setEnv(t, env)
	if st, err := loadSettings(); err != nil || st.port != 18054 {
		t.Fatalf("puerto del entorno: %d %v", st.port, err)
	}
}

func TestLoadSettingsNoArrancaConConfiguracionInvalida(t *testing.T) {
	cases := map[string]func(map[string]string){
		"falta una variable":         func(e map[string]string) { delete(e, "REPUTATION_BOUNCE_WARN") },
		"warn igual a block":         func(e map[string]string) { e["REPUTATION_BOUNCE_WARN"] = "0.04" },
		"queja warn mayor que block": func(e map[string]string) { e["REPUTATION_COMPLAINT_WARN"] = "0.001" },
		"fraccion cero":              func(e map[string]string) { e["REPUTATION_BOUNCE_WARN"] = "0" },
		"fraccion uno":               func(e map[string]string) { e["REPUTATION_BOUNCE_BLOCK"] = "1" },
		"fraccion mayor que uno":     func(e map[string]string) { e["REPUTATION_BOUNCE_BLOCK"] = "1.5" },
		"fraccion negativa":          func(e map[string]string) { e["REPUTATION_COMPLAINT_WARN"] = "-0.0005" },
		"fraccion no numerica":       func(e map[string]string) { e["REPUTATION_COMPLAINT_BLOCK"] = "alto" },
		"ventana en horas sueltas":   func(e map[string]string) { e["REPUTATION_WINDOW"] = "36h" },
		"ventana ilegible":           func(e map[string]string) { e["REPUTATION_WINDOW"] = "siete dias" },
		"ventana de menos de un dia": func(e map[string]string) { e["REPUTATION_WINDOW"] = "12h" },
		"ventana de mas de 365 dias": func(e map[string]string) { e["REPUTATION_WINDOW"] = "8784h" },
		"volumen minimo cero":        func(e map[string]string) { e["REPUTATION_MIN_VOLUME"] = "0" },
		"volumen minimo excesivo":    func(e map[string]string) { e["REPUTATION_MIN_VOLUME"] = "1000000001" },
		"hourly mayor que daily":     func(e map[string]string) { e["REPUTATION_DEFAULT_HOURLY_MARKETING"] = "60000" },
		"limite negativo":            func(e map[string]string) { e["REPUTATION_DEFAULT_DAILY_TRANSACTIONAL"] = "-1" },
		"limite excesivo":            func(e map[string]string) { e["REPUTATION_DEFAULT_DAILY_MARKETING"] = "1000000001" },
		"limite decimal":             func(e map[string]string) { e["REPUTATION_DEFAULT_HOURLY_TRANSACTIONAL"] = "2000.5" },
		"billing sin esquema":        func(e map[string]string) { e["BILLING_URL"] = "billing:8055" },
		"billing ausente":            func(e map[string]string) { delete(e, "BILLING_URL") },
		"puerto no valido":           func(e map[string]string) { e["REPUTATION_PORT"] = "puerto" },
		"puerto fuera de rango":      func(e map[string]string) { e["REPUTATION_PORT"] = "65536" },
	}
	for name, mutate := range cases {
		env := validEnv()
		mutate(env)
		setEnv(t, env)
		if _, err := loadSettings(); err == nil {
			t.Errorf("%s: el servicio no debe arrancar", name)
		}
	}
}

// NaN no es menor ni mayor que nada y pasaria cualquier comparacion de umbral, y un umbral
// infinito no bloquea nunca: ninguno llega a la politica, y el error nombra la variable.
func TestLoadSettingsUmbralesFinitos(t *testing.T) {
	for _, value := range []string{"NaN", "nan", "Inf", "+Inf", "-Inf", "1e400"} {
		env := validEnv()
		env["REPUTATION_BOUNCE_BLOCK"] = value
		setEnv(t, env)
		if _, err := loadSettings(); err == nil || !strings.Contains(err.Error(), "REPUTATION_BOUNCE_BLOCK") {
			t.Errorf("REPUTATION_BOUNCE_BLOCK=%q: %v; se esperaba un error que la nombre", value, err)
		}
	}
}

func TestLoadSettingsInformaTodosLosErrores(t *testing.T) {
	env := validEnv()
	delete(env, "REPUTATION_MIN_VOLUME")
	env["REPUTATION_WINDOW"] = "36h"
	env["REPUTATION_DEFAULT_DAILY_MARKETING"] = "0"
	setEnv(t, env)
	_, err := loadSettings()
	for _, key := range []string{"REPUTATION_MIN_VOLUME", "REPUTATION_WINDOW", "REPUTATION_DEFAULT_DAILY_MARKETING"} {
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("se esperaba el error de %s: %v", key, err)
		}
	}
}
