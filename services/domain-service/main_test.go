package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func setSettingsEnv(t *testing.T, environment, token string) {
	t.Helper()
	for key, value := range map[string]string{
		"MAIL_HOSTNAME":               "mail.cfm.test",
		"MAIL_MX_HOSTNAME":            "mx.cfm.test",
		"MAIL_SPF_INCLUDE":            "include:spf.cfm.test",
		"MAIL_DMARC_RUA":              "dmarc@cfm.test",
		"MAIL_DIRECTORY_URL":          "http://mail-directory:8040",
		"MAIL_SECURITY_URL":           "http://mail-security:8042",
		"ENVIRONMENT":                 environment,
		"INTERNAL_GATEWAY_TOKEN":      token,
		tenantcell.BaseCellEnv:        "",
		mailDirectoryCellHostsEnv:     "",
		mailSecurityCellHostsEnv:      "",
		"ORGANIZATION_URL":            "http://organization:8003",
		"ACCESS_CONTROL_URL":          "",
		"DOMAIN_SERVICE_PORT":         "",
		"DOMAIN_RECHECK_INTERVAL":     "",
		"DOMAIN_SWEEP_TENANT_TIMEOUT": "",
		"DOMAIN_SWEEP_CONCURRENCY":    "",
		"MAIL_DKIM_ROTATION_GRACE":    "",
		"DOMAIN_CHECK_RETENTION":      "",
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

func TestLoadSettingsValoresPorDefecto(t *testing.T) {
	setSettingsEnv(t, "staging", "gateway-token-0123456789")
	st, err := loadSettings(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if st.port != defaultPort || st.recheckInterval != 6*time.Hour || st.sweepTimeout != 5*time.Minute ||
		st.sweepWorkers != 4 || st.rotationGrace != 168*time.Hour || st.checkRetention != 30*24*time.Hour {
		t.Errorf("puerto %d, intervalo %s, tiempo por empresa %s, trabajadores %d, gracia %s, retencion %s",
			st.port, st.recheckInterval, st.sweepTimeout, st.sweepWorkers, st.rotationGrace, st.checkRetention)
	}
}

// Fuera de su rango, o ilegible, una variable impide arrancar y el error la nombra. El intervalo
// no pasa del que cuenta la alerta BarridoDeDominiosSinCelda, y el tiempo por empresa, del
// intervalo.
func TestLoadSettingsRangos(t *testing.T) {
	for nombre, c := range map[string]struct {
		env map[string]string
		err string
	}{
		"intervalo en el suelo, con su tiempo por empresa":    {map[string]string{"DOMAIN_RECHECK_INTERVAL": "5m", "DOMAIN_SWEEP_TENANT_TIMEOUT": "5m"}, ""},
		"tiempo por empresa en el suelo":                      {map[string]string{"DOMAIN_SWEEP_TENANT_TIMEOUT": "1m"}, ""},
		"extremos validos":                                    {map[string]string{"DOMAIN_SWEEP_CONCURRENCY": "64", "MAIL_DKIM_ROTATION_GRACE": "720h", "DOMAIN_CHECK_RETENTION": "168h", "DOMAIN_SERVICE_PORT": "65535"}, ""},
		"intervalo mas largo que la alerta":                   {map[string]string{"DOMAIN_RECHECK_INTERVAL": "6h1m"}, "DOMAIN_RECHECK_INTERVAL"},
		"intervalo por debajo del suelo":                      {map[string]string{"DOMAIN_RECHECK_INTERVAL": "4m"}, "DOMAIN_RECHECK_INTERVAL"},
		"intervalo ilegible":                                  {map[string]string{"DOMAIN_RECHECK_INTERVAL": "seis horas"}, "DOMAIN_RECHECK_INTERVAL"},
		"tiempo por empresa mayor que el intervalo":           {map[string]string{"DOMAIN_RECHECK_INTERVAL": "5m", "DOMAIN_SWEEP_TENANT_TIMEOUT": "10m"}, "DOMAIN_SWEEP_TENANT_TIMEOUT"},
		"tiempo por empresa por debajo del suelo":             {map[string]string{"DOMAIN_SWEEP_TENANT_TIMEOUT": "30s"}, "DOMAIN_SWEEP_TENANT_TIMEOUT"},
		"sin trabajadores":                                    {map[string]string{"DOMAIN_SWEEP_CONCURRENCY": "0"}, "DOMAIN_SWEEP_CONCURRENCY"},
		"demasiados trabajadores":                             {map[string]string{"DOMAIN_SWEEP_CONCURRENCY": "65"}, "DOMAIN_SWEEP_CONCURRENCY"},
		"gracia DKIM en el suelo":                             {map[string]string{"MAIL_DKIM_ROTATION_GRACE": "144h"}, ""},
		"gracia DKIM corta":                                   {map[string]string{"MAIL_DKIM_ROTATION_GRACE": "1h"}, "MAIL_DKIM_ROTATION_GRACE"},
		"gracia DKIM de antes, mas corta que la cola":         {map[string]string{"MAIL_DKIM_ROTATION_GRACE": "72h"}, "MAIL_DKIM_ROTATION_GRACE"},
		"gracia DKIM por debajo de la cola mas un TTL":        {map[string]string{"MAIL_DKIM_ROTATION_GRACE": "143h"}, "MAIL_DKIM_ROTATION_GRACE"},
		"gracia DKIM larga":                                   {map[string]string{"MAIL_DKIM_ROTATION_GRACE": "721h"}, "MAIL_DKIM_ROTATION_GRACE"},
		"historial podado dentro de la ventana de pendientes": {map[string]string{"DOMAIN_CHECK_RETENTION": "24h"}, "DOMAIN_CHECK_RETENTION"},
		"historial de mas de un ano":                          {map[string]string{"DOMAIN_CHECK_RETENTION": "8761h"}, "DOMAIN_CHECK_RETENTION"},
		"puerto cero":                                         {map[string]string{"DOMAIN_SERVICE_PORT": "0"}, "DOMAIN_SERVICE_PORT"},
		"puerto fuera de TCP":                                 {map[string]string{"DOMAIN_SERVICE_PORT": "65536"}, "DOMAIN_SERVICE_PORT"},
	} {
		setSettingsEnv(t, "staging", "gateway-token-0123456789")
		for key, value := range c.env {
			t.Setenv(key, value)
		}
		_, err := loadSettings(zap.NewNop())
		if c.err == "" && err != nil {
			t.Errorf("%s: %v", nombre, err)
		}
		if c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)) {
			t.Errorf("%s: se esperaba un error que nombre %s: %v", nombre, c.err, err)
		}
	}
}

// Los destinos base de mail-directory y mail-security y access-control son URLs base internas
// (config.ServiceURL): mal formadas o, las dos primeras, ausentes, impiden arrancar.
func TestLoadSettingsURLsInternas(t *testing.T) {
	for nombre, c := range map[string]struct {
		key, value, err string
	}{
		"mail-directory con barra final":      {"MAIL_DIRECTORY_URL", "http://mail-directory:8040/", ""},
		"mail-security por https":             {"MAIL_SECURITY_URL", "https://ms.pe-01.internal:8442", ""},
		"access-control propio":               {"ACCESS_CONTROL_URL", "http://127.0.0.1:18002", ""},
		"sin mail-directory":                  {"MAIL_DIRECTORY_URL", "", "MAIL_DIRECTORY_URL"},
		"sin mail-security":                   {"MAIL_SECURITY_URL", " ", "MAIL_SECURITY_URL"},
		"mail-directory sin esquema":          {"MAIL_DIRECTORY_URL", "mail-directory:8040", "MAIL_DIRECTORY_URL"},
		"mail-security con ruta":              {"MAIL_SECURITY_URL", "http://mail-security:8042/api", "MAIL_SECURITY_URL"},
		"mail-security con credenciales":      {"MAIL_SECURITY_URL", "http://u:p@mail-security:8042", "MAIL_SECURITY_URL"},
		"mail-directory con puerto invalido":  {"MAIL_DIRECTORY_URL", "http://mail-directory:0", "MAIL_DIRECTORY_URL"},
		"access-control con consulta":         {"ACCESS_CONTROL_URL", "http://access-control:8002?x=1", "ACCESS_CONTROL_URL"},
		"access-control de otro esquema":      {"ACCESS_CONTROL_URL", "ftp://access-control:8002", "ACCESS_CONTROL_URL"},
		"mail-directory con host con espacio": {"MAIL_DIRECTORY_URL", "http://mail directory:8040", "MAIL_DIRECTORY_URL"},
	} {
		setSettingsEnv(t, "staging", "gateway-token-0123456789")
		t.Setenv(c.key, c.value)
		st, err := loadSettings(zap.NewNop())
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: se esperaba un error que nombre %s: %v", nombre, c.err, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", nombre, err)
			continue
		}
		if st.perms == nil {
			t.Errorf("%s: sin comprobador de permisos", nombre)
		}
	}

	setSettingsEnv(t, "staging", "gateway-token-0123456789")
	t.Setenv("MAIL_DIRECTORY_URL", "http://mail-directory:8040/")
	st, err := loadSettings(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if st.mailDirectoryURL != "http://mail-directory:8040" || st.mailSecurityURL != "http://mail-security:8042" {
		t.Errorf("destinos base: %q %q", st.mailDirectoryURL, st.mailSecurityURL)
	}
}

// Las instancias por celda se declaran con las mismas variables y reglas que el gateway.
// organization hace falta siempre (el indice de dominios); con una celda no se le pregunta la
// celda de ninguna empresa.
func TestLoadSettingsCeldas(t *testing.T) {
	for nombre, c := range map[string]struct {
		base, directory, security, org, err string
	}{
		"una celda: nada que declarar":         {"", "", "", "http://organization:8003", ""},
		"una celda sin organization":           {"", "", "", "", "ORGANIZATION_URL"},
		"organization con una URL sin esquema": {"", "", "", "organization:8003", "ORGANIZATION_URL"},
		"varias celdas":                        {"pe-01", "pe-02=md-pe-02:8040", "pe-02=ms-pe-02:8042,pe-03=ms-pe-03:8042", "http://organization:8003", ""},
		"varias celdas sin organization":       {"pe-01", "pe-02=md-pe-02:8040", "", "", "ORGANIZATION_URL"},
		"solo la celda base":                   {"pe-01", "", "", "http://organization:8003", ""},
		"instancias sin celda base":            {"", "pe-02=md-pe-02:8040", "", "http://organization:8003", tenantcell.BaseCellEnv},
		"celda base tambien como instancia":    {"pe-01", "", "pe-01=ms-pe-01:8042", "http://organization:8003", mailSecurityCellHostsEnv},
		"instancia mal formada":                {"pe-01", "pe-02", "", "http://organization:8003", mailDirectoryCellHostsEnv},
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
