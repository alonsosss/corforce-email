package main

import (
	"strings"
	"testing"
	"time"
)

const (
	testKey    = "k-0123456789abcdef0123456789abcdef"
	testMaster = "m-0123456789abcdef0123456789abcdef"
)

func baseEnv() map[string]string {
	return map[string]string{
		"ENVIRONMENT":                    "production",
		"MAIL_MIGRATION_RUNNER_KEY":      testKey,
		"MIGRATION_API_URL":              "http://mail-migration:8057",
		"MIGRATION_DEST_HOST":            "dovecot",
		"MIGRATION_DEST_TLS_SERVER_NAME": "mail.example.test",
		"DOVECOT_MIGRATION_MASTER_USER":  "migracion",
		"DOVECOT_MIGRATION_MASTER_PASS":  testMaster,
		"MIGRATION_CLAMD_ADDR":           "clamav:3310",
	}
}

func load(env map[string]string) (Config, State, error) {
	prev := executable
	executable = func() (string, error) { return "/usr/local/bin/migration-runner", nil }
	defer func() { executable = prev }()
	return LoadConfig(func(k string) string { return env[k] })
}

func TestConfigCompletaEsActiva(t *testing.T) {
	cfg, state, err := load(baseEnv())
	if state != StateActive || err != nil {
		t.Fatalf("estado %v, error %v", state, err)
	}
	if cfg.DestPort != 993 || cfg.JobTimeout != 24*time.Hour || cfg.PollInterval != 15*time.Second || cfg.ImapsyncBin != "/usr/bin/imapsync" || cfg.WorkDir != "/run/migration" {
		t.Fatalf("defectos: %+v", cfg)
	}
	if cfg.APIURL.String() != "http://mail-migration:8057" || cfg.RunnerID == "" {
		t.Fatalf("api %v runner %q", cfg.APIURL, cfg.RunnerID)
	}
}

func TestConfigSinClaveEstaDesactivada(t *testing.T) {
	env := map[string]string{"ENVIRONMENT": "production"}
	_, state, err := load(env)
	if state != StateDisabled || err != nil {
		t.Fatalf("sin clave debe quedar desactivada sin error: estado %v, error %v", state, err)
	}
}

func TestConfigFuentesPrivadasSoloEnDesarrolloOPrueba(t *testing.T) {
	for _, envName := range []string{"production", "staging", ""} {
		env := baseEnv()
		env["ENVIRONMENT"] = envName
		env["MIGRATION_ALLOW_PRIVATE_SOURCES"] = "true"
		if _, state, err := load(env); state != StateMisconfigured || err == nil || !strings.Contains(err.Error(), "MIGRATION_ALLOW_PRIVATE_SOURCES") {
			t.Fatalf("ENVIRONMENT=%q: estado %v, error %v", envName, state, err)
		}
	}
	// Tambien sin clave: no debe quedar esperando con la opcion peligrosa puesta.
	env := map[string]string{"ENVIRONMENT": "production", "MIGRATION_ALLOW_PRIVATE_SOURCES": "true"}
	if _, state, _ := load(env); state != StateMisconfigured {
		t.Fatalf("sin clave, estado %v", state)
	}
	for _, envName := range []string{"development", "test", "TEST"} {
		env := baseEnv()
		env["ENVIRONMENT"] = envName
		env["MIGRATION_ALLOW_PRIVATE_SOURCES"] = "true"
		cfg, state, err := load(env)
		if state != StateActive || err != nil || !cfg.AllowPrivateSources {
			t.Fatalf("ENVIRONMENT=%q: estado %v, error %v", envName, state, err)
		}
	}
}

func TestConfigAntivirusFallaCerrado(t *testing.T) {
	env := baseEnv()
	delete(env, "MIGRATION_CLAMD_ADDR")
	if _, state, err := load(env); state != StateMisconfigured || err == nil || !strings.Contains(err.Error(), "MIGRATION_CLAMD_ADDR") {
		t.Fatalf("sin clamd debe negarse a reclamar: estado %v, error %v", state, err)
	}
	env["MIGRATION_ALLOW_UNSCANNED"] = "true"
	cfg, state, err := load(env)
	if state != StateActive || err != nil || cfg.ClamdAddr != "" || !cfg.AllowUnscanned {
		t.Fatalf("con la opcion explicita debe activarse: estado %v, error %v", state, err)
	}
	env["MIGRATION_CLAMD_ADDR"] = "sin-puerto"
	if _, state, _ := load(env); state != StateMisconfigured {
		t.Fatalf("clamd sin puerto: estado %v", state)
	}
}

func TestConfigRechazaValoresInvalidos(t *testing.T) {
	cases := map[string]map[string]string{
		"clave corta":            {"MAIL_MIGRATION_RUNNER_KEY": "corta"},
		"sin url":                {"MIGRATION_API_URL": ""},
		"url con credenciales":   {"MIGRATION_API_URL": "http://u:p@host:8057"},
		"url de otro esquema":    {"MIGRATION_API_URL": "ftp://host"},
		"url con parametros":     {"MIGRATION_API_URL": "http://host:8057?x=1"},
		"sin destino":            {"MIGRATION_DEST_HOST": ""},
		"sin nombre TLS destino": {"MIGRATION_DEST_TLS_SERVER_NAME": ""},
		"maestro invalido":       {"DOVECOT_MIGRATION_MASTER_USER": "Mayus*culas"},
		"maestro sin usuario":    {"DOVECOT_MIGRATION_MASTER_USER": ""},
		"maestro corto":          {"DOVECOT_MIGRATION_MASTER_PASS": "corto"},
		"puerto destino":         {"MIGRATION_DEST_PORT": "70000"},
		"plazo fuera de rango":   {"MIGRATION_JOB_TIMEOUT": "10s"},
		"sondeo fuera de rango":  {"MIGRATION_POLL_INTERVAL": "10ms"},
		"booleano invalido":      {"MIGRATION_ALLOW_UNSCANNED": "quiza"},
		"imapsync relativo":      {"MIGRATION_IMAPSYNC_BIN": "imapsync"},
		"directorio con shell":   {"MIGRATION_WORK_DIR": "/run/mig $(x)"},
	}
	for name, override := range cases {
		t.Run(name, func(t *testing.T) {
			env := baseEnv()
			for k, v := range override {
				env[k] = v
			}
			if _, state, err := load(env); state != StateMisconfigured || err == nil {
				t.Fatalf("debe rechazarse: estado %v, error %v", state, err)
			}
		})
	}
}

func TestConfigLosErroresNoRevelanSecretos(t *testing.T) {
	env := baseEnv()
	env["DOVECOT_MIGRATION_MASTER_PASS"] = "corto-secreto"
	env["MAIL_MIGRATION_RUNNER_KEY"] = "clave-corta-secreta"
	_, _, err := load(env)
	if err == nil {
		t.Fatal("debe fallar")
	}
	for _, secret := range []string{"corto-secreto", "clave-corta-secreta"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("el error revela un secreto: %v", err)
		}
	}
}

func TestConfigRutaDelBinarioDebeSerSegura(t *testing.T) {
	prev := executable
	defer func() { executable = prev }()
	executable = func() (string, error) { return "/tmp/con espacio/migration-runner", nil }
	env := baseEnv()
	if _, state, err := LoadConfig(func(k string) string { return env[k] }); state != StateMisconfigured || err == nil {
		t.Fatalf("estado %v, error %v", state, err)
	}
}
