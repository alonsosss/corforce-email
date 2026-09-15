package main

import (
	"strings"
	"testing"
	"time"
)

var settingsKeys = []string{"BILLING_PORT", "BILLING_DEFAULT_PLAN_CODE", "BILLING_TRIAL_DAYS", "BILLING_ENFORCE",
	"BILLING_PERIOD_SWEEP_INTERVAL", "BILLING_PROCESSED_EVENTS_RETENTION"}

func setSettingsEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, k := range settingsKeys {
		t.Setenv(k, env[k])
	}
}

func TestLoadSettingsDefectos(t *testing.T) {
	setSettingsEnv(t, nil)
	st, err := loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if st.port != 8055 || st.business.TrialDays != 0 || st.business.Enforce || st.business.DefaultPlanCode != "" ||
		st.sweepInterval != 15*time.Minute || st.processedRetention != 720*time.Hour {
		t.Fatalf("defectos: %+v", st)
	}
}

// Un valor fuera de su rango impide arrancar; los extremos se admiten.
func TestLoadSettingsRangos(t *testing.T) {
	refused := map[string][]string{
		"BILLING_PORT":                       {"0", "65536", "abc"},
		"BILLING_TRIAL_DAYS":                 {"-1", "367", "7d"},
		"BILLING_PERIOD_SWEEP_INTERVAL":      {"59s", "25h", "15"},
		"BILLING_PROCESSED_EVENTS_RETENTION": {"167h", "8761h", "30d"},
	}
	for key, values := range refused {
		for _, value := range values {
			setSettingsEnv(t, map[string]string{key: value})
			if _, err := loadSettings(); err == nil || !strings.Contains(err.Error(), key) {
				t.Errorf("%s=%q deberia impedir el arranque: %v", key, value, err)
			}
		}
	}
	for _, env := range []map[string]string{
		{"BILLING_TRIAL_DAYS": "0", "BILLING_PERIOD_SWEEP_INTERVAL": "1m", "BILLING_PROCESSED_EVENTS_RETENTION": "168h"},
		{"BILLING_TRIAL_DAYS": "366", "BILLING_PERIOD_SWEEP_INTERVAL": "24h", "BILLING_PROCESSED_EVENTS_RETENTION": "8760h"},
	} {
		setSettingsEnv(t, env)
		if _, err := loadSettings(); err != nil {
			t.Errorf("%v: %v", env, err)
		}
	}
}
