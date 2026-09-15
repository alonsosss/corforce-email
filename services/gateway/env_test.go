package main

import (
	"strings"
	"testing"
	"time"
)

var settingsKeys = []string{"GATEWAY_PORT", "API_RATE_LIMIT_PER_MIN", "AUTH_RATE_LIMIT_PER_MIN", "EXFIL_READ_THRESHOLD", "EXFIL_WINDOW_MIN"}

func setSettingsEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, k := range settingsKeys {
		t.Setenv(k, env[k])
	}
}

// Sin variables, el gateway arranca con sus valores por defecto; los extremos de cada rango se
// admiten.
func TestLoadSettingsDefectosYExtremos(t *testing.T) {
	setSettingsEnv(t, nil)
	st, err := loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if st != (settings{port: 8080, apiRatePerMin: 600, authRatePerMin: 30, exfilReads: 400, exfilWindow: 5 * time.Minute}) {
		t.Fatalf("defectos: %+v", st)
	}
	setSettingsEnv(t, map[string]string{
		"GATEWAY_PORT": "65535", "API_RATE_LIMIT_PER_MIN": "60000", "AUTH_RATE_LIMIT_PER_MIN": "600",
		"EXFIL_READ_THRESHOLD": "100000", "EXFIL_WINDOW_MIN": "60",
	})
	st, err = loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if st != (settings{port: 65535, apiRatePerMin: 60000, authRatePerMin: 600, exfilReads: 100000, exfilWindow: time.Hour}) {
		t.Fatalf("maximos: %+v", st)
	}
}

// Un puerto, un limite o un umbral mal escrito impide arrancar: caer en silencio al valor por
// defecto dejaria el gateway con un cupo que nadie eligio.
func TestLoadSettingsRangos(t *testing.T) {
	refused := map[string][]string{
		"GATEWAY_PORT":            {"0", "65536", "80a"},
		"API_RATE_LIMIT_PER_MIN":  {"0", "-5", "60001", "abc", "4.5"},
		"AUTH_RATE_LIMIT_PER_MIN": {"0", "601", "treinta"},
		"EXFIL_READ_THRESHOLD":    {"0", "100001"},
		"EXFIL_WINDOW_MIN":        {"0", "61", "5m"},
	}
	for key, values := range refused {
		for _, value := range values {
			setSettingsEnv(t, map[string]string{key: value})
			if _, err := loadSettings(); err == nil || !strings.Contains(err.Error(), key) {
				t.Errorf("%s=%q deberia impedir el arranque: %v", key, value, err)
			}
		}
	}
}

// Las rutas del cupo estricto gastan tambien el general: un estricto mayor no frenaria nada.
// Bajar el general por debajo del estricto por defecto obliga a bajar tambien el estricto.
func TestLoadSettingsEstrictoNoSuperaGeneral(t *testing.T) {
	for _, env := range []map[string]string{
		{"API_RATE_LIMIT_PER_MIN": "100", "AUTH_RATE_LIMIT_PER_MIN": "200"},
		{"API_RATE_LIMIT_PER_MIN": "20"},
	} {
		setSettingsEnv(t, env)
		_, err := loadSettings()
		if err == nil || !strings.Contains(err.Error(), "AUTH_RATE_LIMIT_PER_MIN") || !strings.Contains(err.Error(), "API_RATE_LIMIT_PER_MIN") {
			t.Errorf("%v deberia impedir el arranque nombrando los dos cupos: %v", env, err)
		}
	}
	setSettingsEnv(t, map[string]string{"API_RATE_LIMIT_PER_MIN": "50", "AUTH_RATE_LIMIT_PER_MIN": "50"})
	if st, err := loadSettings(); err != nil || st.apiRatePerMin != 50 || st.authRatePerMin != 50 {
		t.Fatalf("cupos iguales: %+v, %v", st, err)
	}
}
