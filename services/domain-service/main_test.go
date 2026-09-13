package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func setSettingsEnv(t *testing.T, environment, token string) {
	t.Helper()
	for key, value := range map[string]string{
		"MAIL_HOSTNAME":           "mail.cfm.test",
		"MAIL_MX_HOSTNAME":        "mx.cfm.test",
		"MAIL_SPF_INCLUDE":        "include:spf.cfm.test",
		"MAIL_DMARC_RUA":          "dmarc@cfm.test",
		"MAIL_DIRECTORY_URL":      "http://mail-directory:8040",
		"MAIL_SECURITY_URL":       "http://mail-security:8042",
		"ENVIRONMENT":             environment,
		"INTERNAL_GATEWAY_TOKEN":  token,
		tenantcell.BaseCellEnv:    "",
		mailDirectoryCellHostsEnv: "",
		mailSecurityCellHostsEnv:  "",
		"ORGANIZATION_URL":        "",
	} {
		t.Setenv(key, value)
	}
}

func TestLoadSettingsSinTokenInternoSoloEnDesarrollo(t *testing.T) {
	cases := map[string]bool{"development": true, "Test": true, "production": false, "staging": false, "": false, "prod": false}
	for environment, starts := range cases {
		setSettingsEnv(t, environment, "")
		_, err := loadSettings(zap.NewNop())
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
	st, err := loadSettings(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if st.internalToken != "gateway-token-0123456789" {
		t.Fatalf("token %q", st.internalToken)
	}
}

// Las instancias por celda se declaran con las mismas variables y reglas que el gateway; con
// varias celdas hace falta organization, y con una no se pregunta a nadie.
func TestLoadSettingsCeldas(t *testing.T) {
	for nombre, c := range map[string]struct {
		base, directory, security, org, err string
	}{
		"una celda: nada que declarar ni organization": {},
		"varias celdas":                     {"pe-01", "pe-02=md-pe-02:8040", "pe-02=ms-pe-02:8042,pe-03=ms-pe-03:8042", "http://organization:8003", ""},
		"varias celdas sin organization":    {"pe-01", "pe-02=md-pe-02:8040", "", "", "ORGANIZATION_URL"},
		"solo la celda base":                {"pe-01", "", "", "http://organization:8003", ""},
		"instancias sin celda base":         {"", "pe-02=md-pe-02:8040", "", "http://organization:8003", tenantcell.BaseCellEnv},
		"celda base tambien como instancia": {"pe-01", "", "pe-01=ms-pe-01:8042", "http://organization:8003", mailSecurityCellHostsEnv},
		"instancia mal formada":             {"pe-01", "pe-02", "", "http://organization:8003", mailDirectoryCellHostsEnv},
	} {
		setSettingsEnv(t, "staging", "gateway-token-0123456789")
		t.Setenv(tenantcell.BaseCellEnv, c.base)
		t.Setenv(mailDirectoryCellHostsEnv, c.directory)
		t.Setenv(mailSecurityCellHostsEnv, c.security)
		t.Setenv("ORGANIZATION_URL", c.org)
		st, err := loadSettings(zap.NewNop())
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: se esperaba error con %s: %v", nombre, c.err, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", nombre, err)
			continue
		}
		if got := st.directoryTargets.BaseCell(); got != c.base || st.securityTargets.BaseCell() != c.base {
			t.Errorf("%s: celda base %q", nombre, got)
		}
		if c.base == "" {
			target, cell, err := st.directoryTargets.For(context.Background(), uuid.NewString())
			if err != nil || target != "http://mail-directory:8040" || cell != "" {
				t.Errorf("%s: una celda va al destino base: %q %q %v", nombre, target, cell, err)
			}
		}
	}

	setSettingsEnv(t, "staging", "gateway-token-0123456789")
	t.Setenv(tenantcell.BaseCellEnv, "pe-01")
	t.Setenv(mailDirectoryCellHostsEnv, "pe-02=md-pe-02:8040")
	t.Setenv(mailSecurityCellHostsEnv, "pe-02=ms-pe-02:8042,pe-03=ms-pe-03:8042")
	t.Setenv("ORGANIZATION_URL", "http://organization:8003")
	st, err := loadSettings(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(st.directoryTargets.URLs(), ","); got != "http://mail-directory:8040,http://md-pe-02:8040" {
		t.Errorf("destinos de mail-directory: %s", got)
	}
	if got := strings.Join(st.securityTargets.Cells(), ","); got != "pe-02,pe-03" {
		t.Errorf("celdas de mail-security: %s", got)
	}
}
