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

func lookup(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

func TestLoadSettingsValida(t *testing.T) {
	st, err := loadSettings(lookup(validEnv()))
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
	if st, err := loadSettings(lookup(env)); err != nil || st.port != 18054 {
		t.Fatalf("puerto del entorno: %d %v", st.port, err)
	}
}

func TestLoadSettingsNoArrancaConConfiguracionInvalida(t *testing.T) {
	cases := map[string]func(map[string]string){
		"falta una variable":         func(e map[string]string) { delete(e, "REPUTATION_BOUNCE_WARN") },
		"warn igual a block":         func(e map[string]string) { e["REPUTATION_BOUNCE_WARN"] = "0.04" },
		"queja warn mayor que block": func(e map[string]string) { e["REPUTATION_COMPLAINT_WARN"] = "0.001" },
		"fraccion mayor que uno":     func(e map[string]string) { e["REPUTATION_BOUNCE_BLOCK"] = "1.5" },
		"fraccion no numerica":       func(e map[string]string) { e["REPUTATION_COMPLAINT_BLOCK"] = "alto" },
		"ventana en horas sueltas":   func(e map[string]string) { e["REPUTATION_WINDOW"] = "36h" },
		"ventana ilegible":           func(e map[string]string) { e["REPUTATION_WINDOW"] = "siete dias" },
		"volumen minimo cero":        func(e map[string]string) { e["REPUTATION_MIN_VOLUME"] = "0" },
		"hourly mayor que daily":     func(e map[string]string) { e["REPUTATION_DEFAULT_HOURLY_MARKETING"] = "60000" },
		"limite negativo":            func(e map[string]string) { e["REPUTATION_DEFAULT_DAILY_TRANSACTIONAL"] = "-1" },
		"billing sin esquema":        func(e map[string]string) { e["BILLING_URL"] = "billing:8055" },
		"billing ausente":            func(e map[string]string) { delete(e, "BILLING_URL") },
		"puerto no valido":           func(e map[string]string) { e["REPUTATION_PORT"] = "puerto" },
	}
	for name, mutate := range cases {
		env := validEnv()
		mutate(env)
		if _, err := loadSettings(lookup(env)); err == nil {
			t.Errorf("%s: el servicio no debe arrancar", name)
		}
	}
}

func TestLoadSettingsInformaTodosLosErrores(t *testing.T) {
	env := validEnv()
	delete(env, "REPUTATION_MIN_VOLUME")
	env["REPUTATION_WINDOW"] = "36h"
	_, err := loadSettings(lookup(env))
	if err == nil || !strings.Contains(err.Error(), "REPUTATION_MIN_VOLUME") || !strings.Contains(err.Error(), "REPUTATION_WINDOW") {
		t.Fatalf("se esperaban los dos errores: %v", err)
	}
}
