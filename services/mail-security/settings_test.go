package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
)

func setSettingsEnv(t *testing.T, environment, token string) {
	t.Helper()
	for key, value := range map[string]string{
		"ENVIRONMENT":                       environment,
		"INTERNAL_GATEWAY_TOKEN":            token,
		"ORGANIZATION_URL":                  "http://organization:8003",
		"ACCESS_CONTROL_URL":                "",
		"TRANSACTIONAL_URL":                 "",
		"RSPAMD_CONTROLLER_URL":             "",
		"RSPAMD_CONTROLLER_PASSWORD":        "",
		"RSPAMD_CONTROLLER_ENABLE_PASSWORD": "",
		"MAIL_SECURITY_PORT":                "",
		"MAIL_POLICY_MAPS_PORT":             "",
		"MAIL_POLICY_EXPORT_PORT":           "",
		"MAIL_REDIS_HOST":                   "",
		"MAIL_REDIS_PORT":                   "",
		"MAIL_QUARANTINE_REINJECT_HOST":     "",
		"MAIL_QUARANTINE_REINJECT_PORT":     "",
		"MAIL_LOG_LINES":                    "",
		"MAIL_QUARANTINE_MAX_BODY_MB":       "",
		"MAIL_REDIS_RECONCILE_INTERVAL":     "",
		"MAIL_DKIM_RECONCILE_INTERVAL":      "",
		"MAIL_QUARANTINE_NOTIFY_INTERVAL":   "",
		"MAIL_QUARANTINE_LINK_TTL":          "",
	} {
		t.Setenv(key, value)
	}
}

func TestLoadSettingsValoresPorDefecto(t *testing.T) {
	setSettingsEnv(t, "staging", "gateway-token-0123456789")
	st, err := loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if st.port != 8042 || st.mapsPort != 8081 || st.exportPort != 9081 || st.redisHost != "redis" || st.redisPort != 6379 ||
		st.reinjectAddr != "postfix:590" || st.logLines != 9999 || st.pipeMaxBodyMiB != 50 {
		t.Errorf("puertos %d %d %d, redis %s:%d, reinyeccion %s, lineas %d, cuerpo %d MiB",
			st.port, st.mapsPort, st.exportPort, st.redisHost, st.redisPort, st.reinjectAddr, st.logLines, st.pipeMaxBodyMiB)
	}
	if st.reconcileInterval != 10*time.Minute || st.dkimReconcileInterval != 15*time.Minute ||
		st.quarantineNotifyInterval != 15*time.Minute || st.quarantineLinkTTL != 72*time.Hour {
		t.Errorf("reconciliacion %s, repaso DKIM %s, aviso %s, enlaces %s",
			st.reconcileInterval, st.dkimReconcileInterval, st.quarantineNotifyInterval, st.quarantineLinkTTL)
	}
	if st.controllerURL != "http://rspamd:11334" || st.transactionalURL != "" || st.organizationURL != "http://organization:8003" {
		t.Errorf("controller %q, transactional %q, organization %q", st.controllerURL, st.transactionalURL, st.organizationURL)
	}
	if st.internalToken != "gateway-token-0123456789" || st.perms == nil {
		t.Errorf("token %q, comprobador de permisos %v", st.internalToken, st.perms != nil)
	}
}

// El token interno es el de middleware.InternalGatewayToken: vacio solo en desarrollo o prueba.
func TestLoadSettingsSinTokenInternoSoloEnDesarrollo(t *testing.T) {
	for environment, starts := range map[string]bool{"development": true, "test": true, "production": false, "staging": false, "": false} {
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

// Fuera de su rango, ilegible o mal formada, una variable impide arrancar y el error la nombra. El
// repaso DKIM no pasa del intervalo que cuentan sus alertas.
func TestLoadSettingsRangos(t *testing.T) {
	for nombre, c := range map[string]struct {
		env map[string]string
		err string
	}{
		"extremos inferiores": {map[string]string{"MAIL_SECURITY_PORT": "1", "MAIL_LOG_LINES": "1", "MAIL_QUARANTINE_MAX_BODY_MB": "1",
			"MAIL_REDIS_RECONCILE_INTERVAL": "1m", "MAIL_DKIM_RECONCILE_INTERVAL": "1s", "MAIL_QUARANTINE_NOTIFY_INTERVAL": "1m", "MAIL_QUARANTINE_LINK_TTL": "1h"}, ""},
		"extremos superiores": {map[string]string{"MAIL_POLICY_EXPORT_PORT": "65535", "MAIL_LOG_LINES": "10000", "MAIL_QUARANTINE_MAX_BODY_MB": "101",
			"MAIL_REDIS_RECONCILE_INTERVAL": "1h", "MAIL_DKIM_RECONCILE_INTERVAL": "15m", "MAIL_QUARANTINE_NOTIFY_INTERVAL": "24h", "MAIL_QUARANTINE_LINK_TTL": "720h"}, ""},
		"repaso DKIM del e2e":                   {map[string]string{"MAIL_DKIM_RECONCILE_INTERVAL": "2s"}, ""},
		"hosts y URLs propios":                  {map[string]string{"MAIL_REDIS_HOST": "127.0.0.1", "MAIL_QUARANTINE_REINJECT_HOST": "fd00::25", "RSPAMD_CONTROLLER_URL": "http://rspamd-mail:11334/", "TRANSACTIONAL_URL": "http://127.0.0.1:18045"}, ""},
		"puerto cero":                           {map[string]string{"MAIL_SECURITY_PORT": "0"}, "MAIL_SECURITY_PORT"},
		"puerto de mapas fuera de TCP":          {map[string]string{"MAIL_POLICY_MAPS_PORT": "65536"}, "MAIL_POLICY_MAPS_PORT"},
		"puerto de exportacion ilegible":        {map[string]string{"MAIL_POLICY_EXPORT_PORT": "9081a"}, "MAIL_POLICY_EXPORT_PORT"},
		"puerto de redis negativo":              {map[string]string{"MAIL_REDIS_PORT": "-1"}, "MAIL_REDIS_PORT"},
		"puerto de reinyeccion cero":            {map[string]string{"MAIL_QUARANTINE_REINJECT_PORT": "0"}, "MAIL_QUARANTINE_REINJECT_PORT"},
		"host de redis con puerto":              {map[string]string{"MAIL_REDIS_HOST": "redis:6379"}, "MAIL_REDIS_HOST"},
		"host de reinyeccion con espacio":       {map[string]string{"MAIL_QUARANTINE_REINJECT_HOST": "post fix"}, "MAIL_QUARANTINE_REINJECT_HOST"},
		"sin lineas de log":                     {map[string]string{"MAIL_LOG_LINES": "0"}, "MAIL_LOG_LINES"},
		"demasiadas lineas de log":              {map[string]string{"MAIL_LOG_LINES": "10001"}, "MAIL_LOG_LINES"},
		"cuerpo de /pipe vacio":                 {map[string]string{"MAIL_QUARANTINE_MAX_BODY_MB": "0"}, "MAIL_QUARANTINE_MAX_BODY_MB"},
		"cuerpo de /pipe mayor que Rspamd":      {map[string]string{"MAIL_QUARANTINE_MAX_BODY_MB": "102"}, "MAIL_QUARANTINE_MAX_BODY_MB"},
		"reconciliacion demasiado seguida":      {map[string]string{"MAIL_REDIS_RECONCILE_INTERVAL": "59s"}, "MAIL_REDIS_RECONCILE_INTERVAL"},
		"reconciliacion de mas de una hora":     {map[string]string{"MAIL_REDIS_RECONCILE_INTERVAL": "61m"}, "MAIL_REDIS_RECONCILE_INTERVAL"},
		"repaso DKIM mas largo que sus alertas": {map[string]string{"MAIL_DKIM_RECONCILE_INTERVAL": "16m"}, "MAIL_DKIM_RECONCILE_INTERVAL"},
		"repaso DKIM por debajo del segundo":    {map[string]string{"MAIL_DKIM_RECONCILE_INTERVAL": "500ms"}, "MAIL_DKIM_RECONCILE_INTERVAL"},
		"repaso DKIM ilegible":                  {map[string]string{"MAIL_DKIM_RECONCILE_INTERVAL": "quince minutos"}, "MAIL_DKIM_RECONCILE_INTERVAL"},
		"aviso demasiado seguido":               {map[string]string{"MAIL_QUARANTINE_NOTIFY_INTERVAL": "30s"}, "MAIL_QUARANTINE_NOTIFY_INTERVAL"},
		"aviso de mas de un dia":                {map[string]string{"MAIL_QUARANTINE_NOTIFY_INTERVAL": "25h"}, "MAIL_QUARANTINE_NOTIFY_INTERVAL"},
		"enlace de menos de una hora":           {map[string]string{"MAIL_QUARANTINE_LINK_TTL": "59m"}, "MAIL_QUARANTINE_LINK_TTL"},
		"enlace de mas de treinta dias":         {map[string]string{"MAIL_QUARANTINE_LINK_TTL": "721h"}, "MAIL_QUARANTINE_LINK_TTL"},
		"enlace negativo":                       {map[string]string{"MAIL_QUARANTINE_LINK_TTL": "-72h"}, "MAIL_QUARANTINE_LINK_TTL"},
		"controller de rspamd con ruta":         {map[string]string{"RSPAMD_CONTROLLER_URL": "http://rspamd:11334/rspamd"}, "RSPAMD_CONTROLLER_URL"},
		"controller de rspamd con credenciales": {map[string]string{"RSPAMD_CONTROLLER_URL": "http://u:p@rspamd:11334"}, "RSPAMD_CONTROLLER_URL"},
		"transactional sin esquema":             {map[string]string{"TRANSACTIONAL_URL": "transactional:8045"}, "TRANSACTIONAL_URL"},
		"transactional con consulta":            {map[string]string{"TRANSACTIONAL_URL": "http://transactional:8045?x=1"}, "TRANSACTIONAL_URL"},
		"access-control con ruta":               {map[string]string{"ACCESS_CONTROL_URL": "http://access-control:8002/api"}, "ACCESS_CONTROL_URL"},
		"organization con ruta":                 {map[string]string{"ORGANIZATION_URL": "http://organization:8003/api"}, "ORGANIZATION_URL"},
		"sin organization":                      {map[string]string{"ORGANIZATION_URL": ""}, "ORGANIZATION_URL"},
		"controller solo con lectura":           {map[string]string{"RSPAMD_CONTROLLER_PASSWORD": strings.Repeat("r", 32)}, ""},
		"controller con lectura y escritura": {map[string]string{"RSPAMD_CONTROLLER_PASSWORD": strings.Repeat("r", 32),
			"RSPAMD_CONTROLLER_ENABLE_PASSWORD": strings.Repeat("w", 32)}, ""},
		"lectura corta":                      {map[string]string{"RSPAMD_CONTROLLER_PASSWORD": strings.Repeat("r", 31)}, "RSPAMD_CONTROLLER_PASSWORD"},
		"lectura que rompe la cabecera":      {map[string]string{"RSPAMD_CONTROLLER_PASSWORD": strings.Repeat("r", 32) + "\r\nX: y"}, "RSPAMD_CONTROLLER_PASSWORD"},
		"escritura con caracter no admitido": {map[string]string{"RSPAMD_CONTROLLER_ENABLE_PASSWORD": strings.Repeat("w", 32) + "!"}, "RSPAMD_CONTROLLER_ENABLE_PASSWORD"},
		"la misma en lectura y escritura": {map[string]string{"RSPAMD_CONTROLLER_PASSWORD": strings.Repeat("x", 32),
			"RSPAMD_CONTROLLER_ENABLE_PASSWORD": strings.Repeat("x", 32)}, "no pueden ser la misma"},
	} {
		setSettingsEnv(t, "staging", "gateway-token-0123456789")
		for key, value := range c.env {
			t.Setenv(key, value)
		}
		_, err := loadSettings()
		if c.err == "" && err != nil {
			t.Errorf("%s: %v", nombre, err)
		}
		if c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)) {
			t.Errorf("%s: se esperaba un error que nombre %s: %v", nombre, c.err, err)
		}
	}
}

// Los tres listeners necesitan puertos distintos: un choque no arranca y el error nombra las dos
// variables, tambien cuando una se queda en su valor por defecto.
func TestLoadSettingsPuertosDistintos(t *testing.T) {
	for nombre, c := range map[string]struct {
		env  map[string]string
		pair []string
	}{
		"mapas en el de administracion por defecto": {map[string]string{"MAIL_POLICY_MAPS_PORT": "8042"},
			[]string{"MAIL_SECURITY_PORT", "MAIL_POLICY_MAPS_PORT"}},
		"administracion en el de exportacion por defecto": {map[string]string{"MAIL_SECURITY_PORT": "9081"},
			[]string{"MAIL_SECURITY_PORT", "MAIL_POLICY_EXPORT_PORT"}},
		"mapas y exportacion iguales": {map[string]string{"MAIL_POLICY_MAPS_PORT": "18081", "MAIL_POLICY_EXPORT_PORT": "18081"},
			[]string{"MAIL_POLICY_MAPS_PORT", "MAIL_POLICY_EXPORT_PORT"}},
		"los tres iguales": {map[string]string{"MAIL_SECURITY_PORT": "18042", "MAIL_POLICY_MAPS_PORT": "18042", "MAIL_POLICY_EXPORT_PORT": "18042"},
			[]string{"MAIL_SECURITY_PORT", "MAIL_POLICY_MAPS_PORT"}},
	} {
		setSettingsEnv(t, "staging", "gateway-token-0123456789")
		for key, value := range c.env {
			t.Setenv(key, value)
		}
		_, err := loadSettings()
		if err == nil || !strings.Contains(err.Error(), c.pair[0]+" y "+c.pair[1]) {
			t.Errorf("%s: se esperaba un error que nombre %s y %s: %v", nombre, c.pair[0], c.pair[1], err)
		}
	}

	setSettingsEnv(t, "staging", "gateway-token-0123456789")
	t.Setenv("MAIL_SECURITY_PORT", "18042")
	t.Setenv("MAIL_POLICY_MAPS_PORT", "18081")
	t.Setenv("MAIL_POLICY_EXPORT_PORT", "19081")
	if st, err := loadSettings(); err != nil || st.port != 18042 || st.mapsPort != 18081 || st.exportPort != 19081 {
		t.Errorf("puertos distintos: %d %d %d, %v", st.port, st.mapsPort, st.exportPort, err)
	}
}

// Las URLs salen como scheme://host[:puerto], sin barra final: los clientes les pegan su ruta.
func TestLoadSettingsNormalizaLasURLs(t *testing.T) {
	setSettingsEnv(t, "staging", "gateway-token-0123456789")
	t.Setenv("RSPAMD_CONTROLLER_URL", " http://rspamd-mail:11334/ ")
	t.Setenv("TRANSACTIONAL_URL", "http://transactional:8045/")
	t.Setenv("ORGANIZATION_URL", "http://organization:8003/")
	t.Setenv("MAIL_QUARANTINE_REINJECT_HOST", "fd00::25")
	st, err := loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if st.controllerURL != "http://rspamd-mail:11334" || st.transactionalURL != "http://transactional:8045" ||
		st.organizationURL != "http://organization:8003" || st.reinjectAddr != "[fd00::25]:590" {
		t.Errorf("controller %q, transactional %q, organization %q, reinyeccion %q",
			st.controllerURL, st.transactionalURL, st.organizationURL, st.reinjectAddr)
	}
}

// Cada contrasena del controller llega a su cliente y a ningun otro: la de lectura a la pantalla del
// antispam y la de escritura al aprendizaje.
func TestLoadSettingsContrasenasDelController(t *testing.T) {
	setSettingsEnv(t, "staging", "gateway-token-0123456789")
	lectura, escritura := strings.Repeat("r", 40), strings.Repeat("w", 40)
	t.Setenv("RSPAMD_CONTROLLER_PASSWORD", " "+lectura+" ")
	t.Setenv("RSPAMD_CONTROLLER_ENABLE_PASSWORD", escritura)
	st, err := loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if st.controllerReadPassword != lectura || st.controllerLearnPassword != escritura {
		t.Errorf("lectura %q, escritura %q", st.controllerReadPassword, st.controllerLearnPassword)
	}

	t.Setenv("RSPAMD_CONTROLLER_ENABLE_PASSWORD", "")
	if st, err = loadSettings(); err != nil || st.controllerLearnPassword != "" || st.controllerReadPassword != lectura {
		t.Errorf("solo lectura: err %v, escritura %q", err, st.controllerLearnPassword)
	}
}
