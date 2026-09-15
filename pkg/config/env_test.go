package config

import (
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

// unsetEnv deja key sin definir durante la prueba y la restaura al terminar.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

func TestEnvInt(t *testing.T) {
	const key = "CONFIG_TEST_ENV_INT"
	cases := []struct {
		name    string
		value   string
		want    int
		wantErr bool
	}{
		{"vacia vale el defecto", "", 7, false},
		{"en blanco vale el defecto", " \t ", 7, false},
		{"espacios en los extremos", " 12 ", 12, false},
		{"minimo incluido", "1", 1, false},
		{"maximo incluido", "20", 20, false},
		{"por debajo", "0", 0, true},
		{"negativo", "-3", 0, true},
		{"por encima", "21", 0, true},
		{"basura", "diez", 0, true},
		{"decimal", "4.5", 0, true},
		{"con unidad", "10s", 0, true},
		{"desborda int", "99999999999999999999", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(key, c.value)
			got, err := EnvInt(key, 7, 1, 20)
			if c.wantErr {
				if err == nil || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "between 1 and 20") {
					t.Fatalf("%q: %d, %v; se esperaba un error que nombre %s y el rango", c.value, got, err, key)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("%q: %d, %v; se esperaba %d", c.value, got, err, c.want)
			}
		})
	}
	unsetEnv(t, key)
	if got, err := EnvInt(key, 7, 1, 20); err != nil || got != 7 {
		t.Fatalf("sin definir: %d, %v; se esperaba el defecto", got, err)
	}
}

func TestEnvDuration(t *testing.T) {
	const key = "CONFIG_TEST_ENV_DURATION"
	cases := []struct {
		name    string
		value   string
		want    time.Duration
		wantErr bool
	}{
		{"vacia vale el defecto", "", 15 * time.Second, false},
		{"en blanco vale el defecto", "  ", 15 * time.Second, false},
		{"espacios en los extremos", " 90s ", 90 * time.Second, false},
		{"unidades compuestas", "1m30s", 90 * time.Second, false},
		{"minimo incluido", "1s", time.Second, false},
		{"maximo incluido", "1h", time.Hour, false},
		{"por debajo", "500ms", 0, true},
		{"cero", "0", 0, true},
		{"negativa", "-1m", 0, true},
		{"por encima", "61m", 0, true},
		{"sin unidad", "30", 0, true},
		{"basura", "quince", 0, true},
		{"unidad que Go no conoce", "1d", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(key, c.value)
			got, err := EnvDuration(key, 15*time.Second, time.Second, time.Hour)
			if c.wantErr {
				if err == nil || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "between 1s and 1h0m0s") {
					t.Fatalf("%q: %v, %v; se esperaba un error que nombre %s y el rango", c.value, got, err, key)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("%q: %v, %v; se esperaba %v", c.value, got, err, c.want)
			}
		})
	}
	unsetEnv(t, key)
	if got, err := EnvDuration(key, 15*time.Second, time.Second, time.Hour); err != nil || got != 15*time.Second {
		t.Fatalf("sin definir: %v, %v; se esperaba el defecto", got, err)
	}
}

// NaN e infinito no son una tasa: NaN pasaria cualquier comparacion de rango y una tasa
// infinita dejaba sin limite el envio a SES.
func TestEnvFloat(t *testing.T) {
	const key = "CONFIG_TEST_ENV_FLOAT"
	cases := []struct {
		name    string
		value   string
		want    float64
		wantErr bool
	}{
		{"vacia vale el defecto", "", 10, false},
		{"espacios en los extremos", " 2.5 ", 2.5, false},
		{"minimo incluido", "0.5", 0.5, false},
		{"maximo incluido", "100", 100, false},
		{"exponente", "1e2", 100, false},
		{"por debajo", "0.4", 0, true},
		{"por encima", "100.5", 0, true},
		{"NaN", "NaN", 0, true},
		{"nan en minusculas", "nan", 0, true},
		{"infinito", "Inf", 0, true},
		{"infinito con signo", "+Inf", 0, true},
		{"menos infinito", "-Inf", 0, true},
		{"desborda a infinito", "1e400", 0, true},
		{"basura", "diez", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(key, c.value)
			got, err := EnvFloat(key, 10, 0.5, 100)
			if c.wantErr {
				if err == nil || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "between 0.5 and 100") {
					t.Fatalf("%q: %v, %v; se esperaba un error que nombre %s y el rango", c.value, got, err, key)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("%q: %v, %v; se esperaba %v", c.value, got, err, c.want)
			}
		})
	}
	unsetEnv(t, key)
	if got, err := EnvFloat(key, 10, 0.5, 100); err != nil || got != 10 {
		t.Fatalf("sin definir: %v, %v; se esperaba el defecto", got, err)
	}
	for _, lim := range [][3]float64{{math.NaN(), 0, 1}, {1, math.NaN(), 2}, {1, 0, math.Inf(1)}, {1, math.Inf(-1), 2}} {
		if _, err := EnvFloat(key, lim[0], lim[1], lim[2]); err == nil || !strings.Contains(err.Error(), "finite") {
			t.Errorf("defecto o rango %v aceptado: %v", lim, err)
		}
	}
}

// Un defecto fuera de su rango, o un rango vacio, es un error de programacion: falla en
// cada llamada, tambien con la variable definida y valida.
func TestEnvDefectoFueraDeRango(t *testing.T) {
	const key = "CONFIG_TEST_ENV_DEFAULT"
	for _, value := range []string{"", "5"} {
		t.Setenv(key, value)
		if _, err := EnvInt(key, 0, 1, 10); err == nil || !strings.Contains(err.Error(), "default") {
			t.Errorf("%q: defecto por debajo aceptado: %v", value, err)
		}
		if _, err := EnvInt(key, 11, 1, 10); err == nil {
			t.Errorf("%q: defecto por encima aceptado", value)
		}
		if _, err := EnvInt(key, 5, 10, 1); err == nil {
			t.Errorf("%q: rango vacio aceptado", value)
		}
		if _, err := EnvDuration(key, time.Hour, time.Second, time.Minute); err == nil || !strings.Contains(err.Error(), "default") {
			t.Errorf("%q: duracion por defecto fuera de rango aceptada: %v", value, err)
		}
	}
}

// loadNumericKeys son las variables numericas y de duracion que leen Load y LoadRedis.
var loadNumericKeys = []string{"POSTGRES_PORT", "POSTGRES_DIRECT_PORT", "REDIS_PORT", "JWT_ACCESS_TTL", "JWT_REFRESH_TTL"}

func TestLoadRangos(t *testing.T) {
	valid := []struct {
		key, value string
		check      func(*Config) bool
	}{
		{"POSTGRES_PORT", "6432", func(c *Config) bool { return c.Postgres.Port == 6432 }},
		{"POSTGRES_DIRECT_PORT", "0", func(c *Config) bool { return c.Postgres.DirectPort == 0 }},
		{"POSTGRES_DIRECT_PORT", "65535", func(c *Config) bool { return c.Postgres.DirectPort == 65535 }},
		{"REDIS_PORT", "1", func(c *Config) bool { return c.Redis.Port == 1 }},
		{"JWT_ACCESS_TTL", "2m", func(c *Config) bool { return c.JWT.AccessTTL == 2*time.Minute }},
		{"JWT_ACCESS_TTL", "5m", func(c *Config) bool { return c.JWT.AccessTTL == 5*time.Minute }},
		{"JWT_REFRESH_TTL", "1h", func(c *Config) bool { return c.JWT.RefreshTTL == time.Hour }},
		{"JWT_REFRESH_TTL", "8760h", func(c *Config) bool { return c.JWT.RefreshTTL == 8760*time.Hour }},
	}
	invalid := map[string][]string{
		"POSTGRES_PORT":        {"0", "65536", "abc", "-5432"},
		"POSTGRES_DIRECT_PORT": {"-1", "65536", "x"},
		"REDIS_PORT":           {"0", "70000", "6379/tcp"},
		// El access token no se alarga por una errata: ni horas ni el antiguo 15m.
		"JWT_ACCESS_TTL":  {"1m", "15m", "1h", "5", "0"},
		"JWT_REFRESH_TTL": {"30m", "8761h", "7d"},
	}
	reset := func(t *testing.T) {
		setEnv(t, map[string]string{"POSTGRES_PASSWORD": "platform-pass"})
		for _, key := range loadNumericKeys {
			t.Setenv(key, "")
		}
	}

	reset(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("sin variables: %v", err)
	}
	if cfg.Postgres.Port != 5432 || cfg.Postgres.DirectPort != 0 || cfg.Redis.Port != 6379 ||
		cfg.JWT.AccessTTL != 5*time.Minute || cfg.JWT.RefreshTTL != 168*time.Hour {
		t.Fatalf("defectos inesperados: %+v %+v %+v", cfg.Postgres, cfg.Redis, cfg.JWT)
	}
	for _, v := range valid {
		reset(t)
		t.Setenv(v.key, v.value)
		cfg, err := Load()
		if err != nil || !v.check(cfg) {
			t.Errorf("%s=%q: %v", v.key, v.value, err)
		}
	}
	for key, values := range invalid {
		for _, value := range values {
			reset(t)
			t.Setenv(key, value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), key) {
				t.Errorf("%s=%q deberia impedir cargar la configuracion: %v", key, value, err)
			}
		}
	}

	reset(t)
	t.Setenv("REDIS_PORT", "0")
	if _, err := LoadRedis(); err == nil || !strings.Contains(err.Error(), "REDIS_PORT") {
		t.Fatalf("LoadRedis con REDIS_PORT=0: %v", err)
	}
}
