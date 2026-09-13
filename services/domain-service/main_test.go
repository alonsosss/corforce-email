package main

import (
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
)

func setSettingsEnv(t *testing.T, environment, token string) {
	t.Helper()
	for key, value := range map[string]string{
		"MAIL_HOSTNAME":          "mail.cfm.test",
		"MAIL_MX_HOSTNAME":       "mx.cfm.test",
		"MAIL_SPF_INCLUDE":       "include:spf.cfm.test",
		"MAIL_DMARC_RUA":         "dmarc@cfm.test",
		"MAIL_DIRECTORY_URL":     "http://mail-directory:8040",
		"MAIL_SECURITY_URL":      "http://mail-security:8042",
		"ENVIRONMENT":            environment,
		"INTERNAL_GATEWAY_TOKEN": token,
	} {
		t.Setenv(key, value)
	}
}

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

func TestLoadSettingsConTokenInterno(t *testing.T) {
	setSettingsEnv(t, "staging", "gateway-token-0123456789")
	st, err := loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if st.internalToken != "gateway-token-0123456789" {
		t.Fatalf("token %q", st.internalToken)
	}
}
