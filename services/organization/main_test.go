package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/organization/internal/app"
)

const tokenDePrueba = "gateway-token-0123456789"

func setSettingsEnv(t *testing.T, environment, token string) {
	t.Helper()
	for key, value := range map[string]string{
		"ENVIRONMENT":                      environment,
		"INTERNAL_GATEWAY_TOKEN":           token,
		"ORGANIZATION_PORT":                "",
		"ORGANIZATION_SAGA_LEASE":          "",
		"ORGANIZATION_SAGA_SWEEP_INTERVAL": "",
	} {
		t.Setenv(key, value)
	}
}

// La saga presenta el token interno a identity, access-control y el mail-directory de cada
// celda: sin el, solo arranca en desarrollo o prueba declarados.
func TestLoadSettingsSinTokenInternoSoloEnDesarrollo(t *testing.T) {
	cases := map[string]bool{"development": true, "Test": true, "production": false, "staging": false, "": false, "prod": false}
	for environment, starts := range cases {
		setSettingsEnv(t, environment, "")
		_, err := loadSettings()
		if starts && err != nil {
			t.Errorf("ENVIRONMENT=%q sin token: %v", environment, err)
		}
		if !starts && !errors.Is(err, middleware.ErrGatewayTokenRequired) {
			t.Errorf("ENVIRONMENT=%q sin token: %v, se esperaba ErrGatewayTokenRequired", environment, err)
		}
	}
}

func TestLoadSettingsValoresPorDefecto(t *testing.T) {
	setSettingsEnv(t, "staging", tokenDePrueba)
	st, err := loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if st.internalToken != tokenDePrueba {
		t.Error("el token interno no es el del entorno")
	}
	if st.port != defaultPort || st.sagaLease != app.DefaultSagaLease || st.sagaSweepInterval != time.Minute {
		t.Errorf("puerto %d, arriendo %s, barrido %s", st.port, st.sagaLease, st.sagaSweepInterval)
	}
}

// Fuera de su rango, o ilegible, una variable impide arrancar y el error la nombra.
func TestLoadSettingsRangos(t *testing.T) {
	for _, c := range []struct {
		key, value string
		ok         bool
	}{
		{"ORGANIZATION_PORT", "1", true},
		{"ORGANIZATION_PORT", "65535", true},
		{"ORGANIZATION_PORT", "0", false},
		{"ORGANIZATION_PORT", "65536", false},
		{"ORGANIZATION_PORT", "ocho", false},
		{"ORGANIZATION_SAGA_LEASE", "2m", true},
		{"ORGANIZATION_SAGA_LEASE", "1h", true},
		{"ORGANIZATION_SAGA_LEASE", "1m", false},
		{"ORGANIZATION_SAGA_LEASE", "61m", false},
		{"ORGANIZATION_SAGA_LEASE", "0", false},
		{"ORGANIZATION_SAGA_SWEEP_INTERVAL", "1s", true},
		{"ORGANIZATION_SAGA_SWEEP_INTERVAL", "1h", true},
		{"ORGANIZATION_SAGA_SWEEP_INTERVAL", "500ms", false},
		{"ORGANIZATION_SAGA_SWEEP_INTERVAL", "2h", false},
		{"ORGANIZATION_SAGA_SWEEP_INTERVAL", "-1m", false},
	} {
		setSettingsEnv(t, "staging", tokenDePrueba)
		t.Setenv(c.key, c.value)
		_, err := loadSettings()
		if c.ok && err != nil {
			t.Errorf("%s=%s: %v", c.key, c.value, err)
		}
		if !c.ok && (err == nil || !strings.Contains(err.Error(), c.key)) {
			t.Errorf("%s=%s: se esperaba un error que nombre la variable: %v", c.key, c.value, err)
		}
	}
}
