package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
)

// setSettingsEnv deja una configuracion valida sin ningun respaldo de desarrollo y le aplica
// overrides; toda variable que lee loadSettings queda fijada para no heredar la del proceso.
func setSettingsEnv(t *testing.T, environment string, overrides map[string]string) {
	t.Helper()
	vars := map[string]string{
		"ENVIRONMENT":                         environment,
		"CELL_CODE":                           "pe-01",
		"MAIL_AUTH_URL":                       "https://mail-auth:9082",
		"MAIL_HOSTNAME":                       "mail.cfm.test",
		"MAIL_DIRECTORY_URL":                  "http://mail-directory:8040",
		"INTERNAL_GATEWAY_TOKEN":              "gateway-token-0123456789",
		"WEBMAIL_IMAP_ADDR":                   "dovecot:993",
		"WEBMAIL_SMTP_ADDR":                   "postfix:587",
		"WEBMAIL_MASTER_USER":                 "webmail@platform.local",
		"WEBMAIL_MASTER_PASSWORD":             strings.Repeat("m", minMasterPasswordLen),
		"WEBMAIL_CLAMD_ADDR":                  "clamd:3310",
		"WEBMAIL_PORT":                        "",
		"WEBMAIL_IMAP_TLS":                    "",
		"WEBMAIL_SMTP_TLS":                    "",
		"WEBMAIL_TLS_SERVER_NAME":             "",
		"WEBMAIL_TLS_CA_FILE":                 "",
		"WEBMAIL_TLS_INSECURE_SKIP_VERIFY":    "",
		"WEBMAIL_ALLOW_UNSCANNED_ATTACHMENTS": "",
		"WEBMAIL_SESSION_IDLE":                "",
		"WEBMAIL_SESSION_MAX":                 "",
		"WEBMAIL_MAX_RECIPIENTS":              "",
		"WEBMAIL_MAX_MESSAGE_BYTES":           "",
		"WEBMAIL_MAX_BODY_PART_BYTES":         "",
		"WEBMAIL_MAX_ATTACHMENT_BYTES":        "",
		"AUTH_COOKIE_SECURE":                  "",
		"CORS_ALLOWED_ORIGINS":                "",
		"API_ORIGIN":                          "",
	}
	for key, value := range overrides {
		vars[key] = value
	}
	for key, value := range vars {
		t.Setenv(key, value)
	}
}

// Cada respaldo de desarrollo del webmail solo se admite con ENVIRONMENT declarado
// development o test; staging, sin declarar o un valor cualquiera no arrancan.
func TestLoadSettingsRespaldosSoloEnDesarrollo(t *testing.T) {
	relaxations := []struct {
		name      string
		overrides map[string]string
		refused   func(error) bool
	}{
		{"TLS sin verificar", map[string]string{"WEBMAIL_TLS_INSECURE_SKIP_VERIFY": "true"}, errorMentions("TLS verificado")},
		{"IMAP sin TLS", map[string]string{"WEBMAIL_IMAP_TLS": "none"}, errorMentions("TLS verificado")},
		{"adjuntos sin ClamAV", map[string]string{"WEBMAIL_CLAMD_ADDR": "", "WEBMAIL_ALLOW_UNSCANNED_ATTACHMENTS": "true"}, errorMentions("WEBMAIL_CLAMD_ADDR")},
		{"sin token interno", map[string]string{"INTERNAL_GATEWAY_TOKEN": ""}, func(err error) bool {
			return errors.Is(err, middleware.ErrGatewayTokenRequired)
		}},
	}
	environments := map[string]bool{"development": true, "test": true, "Development": true, "production": false, "staging": false, "": false, "prod": false}
	for _, r := range relaxations {
		for environment, allowed := range environments {
			setSettingsEnv(t, environment, r.overrides)
			_, err := loadSettings()
			if allowed && err != nil {
				t.Errorf("%s con ENVIRONMENT=%q: %v", r.name, environment, err)
			}
			if !allowed && !r.refused(err) {
				t.Errorf("%s con ENVIRONMENT=%q deberia impedir el arranque: %v", r.name, environment, err)
			}
		}
	}
}

func TestLoadSettingsSinRespaldosArrancaEnCualquierEntorno(t *testing.T) {
	for _, environment := range []string{"production", "staging", "", "development"} {
		setSettingsEnv(t, environment, nil)
		st, err := loadSettings()
		if err != nil {
			t.Fatalf("ENVIRONMENT=%q: %v", environment, err)
		}
		if st.tlsInsecure || st.clamdAddr == "" || st.internalToken == "" {
			t.Fatalf("ENVIRONMENT=%q: configuracion inesperada %+v", environment, st)
		}
	}
}

// Sin ClamAV y sin pedirlo expresamente no arranca ni en desarrollo.
func TestLoadSettingsSinClamAVNiOptOut(t *testing.T) {
	setSettingsEnv(t, "development", map[string]string{"WEBMAIL_CLAMD_ADDR": ""})
	if _, err := loadSettings(); !errorMentions("WEBMAIL_CLAMD_ADDR")(err) {
		t.Fatalf("%v", err)
	}
}

func errorMentions(fragment string) func(error) bool {
	return func(err error) bool { return err != nil && strings.Contains(err.Error(), fragment) }
}

// Un tope o una vida de sesion fuera de su rango impide arrancar; los extremos se admiten.
func TestLoadSettingsRangos(t *testing.T) {
	refused := map[string][]string{
		"WEBMAIL_PORT":                 {"0", "65536", "abc"},
		"WEBMAIL_SESSION_IDLE":         {"30s", "25h", "30"},
		"WEBMAIL_SESSION_MAX":          {"30s", "721h"},
		"WEBMAIL_MAX_RECIPIENTS":       {"0", "1001"},
		"WEBMAIL_MAX_MESSAGE_BYTES":    {"0", "104857601", "25MB"},
		"WEBMAIL_MAX_BODY_PART_BYTES":  {"-1", "104857601"},
		"WEBMAIL_MAX_ATTACHMENT_BYTES": {"104857601"},
	}
	for key, values := range refused {
		for _, value := range values {
			setSettingsEnv(t, "production", map[string]string{key: value})
			if _, err := loadSettings(); !errorMentions(key)(err) {
				t.Errorf("%s=%q deberia impedir el arranque: %v", key, value, err)
			}
		}
	}
	setSettingsEnv(t, "production", map[string]string{
		"WEBMAIL_MAX_RECIPIENTS": "1000", "WEBMAIL_MAX_MESSAGE_BYTES": "104857600",
		"WEBMAIL_MAX_ATTACHMENT_BYTES": "104857600", "WEBMAIL_SESSION_MAX": "720h",
	})
	st, err := loadSettings()
	if err != nil || st.limits.MaxRecipients != 1000 || st.limits.MaxMessageBytes != 104857600 || st.maxAttachmentBytes != 104857600 {
		t.Fatalf("topes maximos: %+v %v", st.limits, err)
	}
}

// La celda va en cada token de sesion: un CELL_CODE que no es un codigo de celda no arranca.
func TestLoadSettingsCeldaInvalida(t *testing.T) {
	for _, cell := range []string{"", "PE-01", "pe.01", "pe_01", "pe-01 x"} {
		setSettingsEnv(t, "production", map[string]string{"CELL_CODE": cell})
		if _, err := loadSettings(); err == nil || !strings.Contains(err.Error(), "CELL_CODE") {
			t.Errorf("CELL_CODE=%q: %v", cell, err)
		}
	}
	setSettingsEnv(t, "production", map[string]string{"CELL_CODE": "pe-02"})
	if st, err := loadSettings(); err != nil || st.cellCode != "pe-02" {
		t.Fatalf("CELL_CODE=pe-02: %q %v", st.cellCode, err)
	}
}
