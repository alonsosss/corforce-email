package main

import (
	"strings"
	"testing"

	"go.uber.org/zap"
)

// baseEnv deja lo minimo para que loadSettings arranque en pruebas.
func baseEnv(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"ENVIRONMENT":            "test",
		"MAIL_DIRECTORY_URL":     "http://mail-directory:8040",
		"ORGANIZATION_URL":       "http://organization:8003",
		"ACCESS_CONTROL_URL":     "http://access-control:8002",
		"INTERNAL_GATEWAY_TOKEN": "token-interno-de-prueba",
	} {
		t.Setenv(k, v)
	}
	for _, k := range []string{
		"MAIL_MIGRATION_RUNNER_KEY", "MAIL_MIGRATION_ALLOW_PRIVATE_SOURCES", "MAIL_MIGRATION_ALLOW_PLAINTEXT",
		"MAIL_MIGRATION_SOURCE_PORTS", "MAIL_MIGRATION_LEASE", "MAIL_MIGRATION_MAX_ATTEMPTS", "MAIL_MIGRATION_SWEEP_INTERVAL", "MAIL_MIGRATION_PORT", "MAIL_MIGRATION_RUNNER_PORT", "MAIL_MIGRATION_MAX_ACTIVE_PER_TENANT",
		"MAIL_DIRECTORY_CELL_HOSTS", "GATEWAY_BASE_CELL_CODE",
	} {
		t.Setenv(k, "")
	}
}

func TestSinClaveDelEjecutorArrancaDesactivado(t *testing.T) {
	baseEnv(t)
	st, err := loadSettings(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if st.app.RunnerConfigured || st.runnerKey != "" {
		t.Fatal("sin clave el servicio no debe declararse configurado")
	}
	if st.port != 8056 || st.runnerPort != 8057 || st.app.MaxActivePerTenant != 2 || len(st.app.Source.Ports) != 2 {
		t.Fatalf("valores por defecto: %+v", st)
	}
	if st.app.Source.AllowPlaintext || st.app.Source.AllowPrivate {
		t.Fatal("TLS obligatorio y solo origenes publicos por defecto")
	}
}

func TestLaClaveDelEjecutorSeValida(t *testing.T) {
	baseEnv(t)
	t.Setenv("MAIL_MIGRATION_RUNNER_KEY", "corta")
	if _, err := loadSettings(zap.NewNop()); err == nil || !strings.Contains(err.Error(), "MAIL_MIGRATION_RUNNER_KEY") {
		t.Fatalf("una clave corta debe rechazarse: %v", err)
	}
	t.Setenv("MAIL_MIGRATION_RUNNER_KEY", strings.Repeat("k", 40))
	st, err := loadSettings(zap.NewNop())
	if err != nil || !st.app.RunnerConfigured {
		t.Fatalf("clave valida: %v", err)
	}
}

func TestLosOrigenesInternosSoloSeAdmitenEnPruebas(t *testing.T) {
	baseEnv(t)
	t.Setenv("MAIL_MIGRATION_ALLOW_PRIVATE_SOURCES", "true")
	st, err := loadSettings(zap.NewNop())
	if err != nil || !st.app.Source.AllowPrivate {
		t.Fatalf("en test debe admitirse: %v", err)
	}
	for _, env := range []string{"production", "staging", ""} {
		t.Setenv("ENVIRONMENT", env)
		if _, err := loadSettings(zap.NewNop()); err == nil || !strings.Contains(err.Error(), "ALLOW_PRIVATE_SOURCES") {
			t.Errorf("ENVIRONMENT=%q: debe negarse a arrancar: %v", env, err)
		}
	}
}

func TestConfiguracionInvalida(t *testing.T) {
	casos := map[string][2]string{
		"puerto de origen":      {"MAIL_MIGRATION_SOURCE_PORTS", "143,imap"},
		"puerto fuera":          {"MAIL_MIGRATION_SOURCE_PORTS", "70000"},
		"booleano":              {"MAIL_MIGRATION_ALLOW_PLAINTEXT", "quizas"},
		"limite en cero":        {"MAIL_MIGRATION_MAX_ACTIVE_PER_TENANT", "0"},
		"lease demasiado corto": {"MAIL_MIGRATION_LEASE", "1s"},
		"puertos iguales":       {"MAIL_MIGRATION_RUNNER_PORT", "8056"},
	}
	for name, kv := range casos {
		baseEnv(t)
		t.Setenv(kv[0], kv[1])
		if _, err := loadSettings(zap.NewNop()); err == nil {
			t.Errorf("%s: se admitio %s=%s", name, kv[0], kv[1])
		}
	}
	baseEnv(t)
	t.Setenv("MAIL_MIGRATION_SOURCE_PORTS", "993")
	t.Setenv("MAIL_MIGRATION_ALLOW_PLAINTEXT", "true")
	st, err := loadSettings(zap.NewNop())
	if err != nil || len(st.app.Source.Ports) != 1 || !st.app.Source.AllowPlaintext {
		t.Fatalf("configuracion valida: %+v %v", st, err)
	}
}
