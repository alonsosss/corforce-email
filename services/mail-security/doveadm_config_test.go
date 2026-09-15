package main

import (
	"strings"
	"testing"

	"go.uber.org/zap"
)

// Sin DOVEADM_API_KEY la revocacion en Dovecot solo se omite con ENVIRONMENT=development|test; con
// cualquier otro (staging y sin declarar incluidos) el servicio no arranca. DOVEADM_API_URL sigue
// la regla de config.ServiceURL, este o no la clave, y con clave tiene que ser https; la clave y el
// nombre del certificado se validan siempre que hay clave.
func TestRevocacionEnDovecotDesdeElEntorno(t *testing.T) {
	clave := strings.Repeat("k", 48)
	cases := []struct {
		name       string
		env        map[string]string
		wantClient bool
		wantErr    string
	}{
		{name: "development sin clave", env: map[string]string{"ENVIRONMENT": "development"}},
		{name: "test sin clave", env: map[string]string{"ENVIRONMENT": "test"}},
		{name: "production sin clave", env: map[string]string{"ENVIRONMENT": "production"}, wantErr: "DOVEADM_API_KEY"},
		{name: "staging sin clave", env: map[string]string{"ENVIRONMENT": "staging"}, wantErr: "DOVEADM_API_KEY"},
		{name: "sin ENVIRONMENT", env: map[string]string{}, wantErr: "DOVEADM_API_KEY"},
		{name: "con clave y MAIL_HOSTNAME", env: map[string]string{"ENVIRONMENT": "production", "DOVEADM_API_KEY": clave, "MAIL_HOSTNAME": "mail.acme.test"}, wantClient: true},
		{name: "con nombre propio del certificado", env: map[string]string{"ENVIRONMENT": "production", "DOVEADM_API_KEY": clave, "DOVEADM_API_TLS_SERVER_NAME": "dovecot.celda.test"}, wantClient: true},
		{name: "clave corta tambien en development", env: map[string]string{"ENVIRONMENT": "development", "DOVEADM_API_KEY": "corta", "MAIL_HOSTNAME": "mail.acme.test"}, wantErr: "32 a 256"},
		{name: "en claro", env: map[string]string{"ENVIRONMENT": "production", "DOVEADM_API_KEY": clave, "MAIL_HOSTNAME": "mail.acme.test", "DOVEADM_API_URL": "http://dovecot:8443"}, wantErr: "https"},
		{name: "con barra final", env: map[string]string{"ENVIRONMENT": "production", "DOVEADM_API_KEY": clave, "MAIL_HOSTNAME": "mail.acme.test", "DOVEADM_API_URL": "https://dovecot.pe-01:9443/"}, wantClient: true},
		{name: "con ruta", env: map[string]string{"ENVIRONMENT": "production", "DOVEADM_API_KEY": clave, "MAIL_HOSTNAME": "mail.acme.test", "DOVEADM_API_URL": "https://dovecot:8443/otra"}, wantErr: "DOVEADM_API_URL"},
		{name: "con credencial", env: map[string]string{"ENVIRONMENT": "production", "DOVEADM_API_KEY": clave, "MAIL_HOSTNAME": "mail.acme.test", "DOVEADM_API_URL": "https://u:p@dovecot:8443"}, wantErr: "DOVEADM_API_URL"},
		{name: "con consulta", env: map[string]string{"ENVIRONMENT": "production", "DOVEADM_API_KEY": clave, "MAIL_HOSTNAME": "mail.acme.test", "DOVEADM_API_URL": "https://dovecot:8443?x=1"}, wantErr: "DOVEADM_API_URL"},
		{name: "sin host", env: map[string]string{"ENVIRONMENT": "production", "DOVEADM_API_KEY": clave, "MAIL_HOSTNAME": "mail.acme.test", "DOVEADM_API_URL": "https://"}, wantErr: "DOVEADM_API_URL"},
		{name: "con puerto invalido", env: map[string]string{"ENVIRONMENT": "production", "DOVEADM_API_KEY": clave, "MAIL_HOSTNAME": "mail.acme.test", "DOVEADM_API_URL": "https://dovecot:70000"}, wantErr: "DOVEADM_API_URL"},
		{name: "mal formada tambien sin clave", env: map[string]string{"ENVIRONMENT": "development", "DOVEADM_API_URL": "dovecot:8443"}, wantErr: "DOVEADM_API_URL"},
		{name: "sin nombre del certificado", env: map[string]string{"ENVIRONMENT": "production", "DOVEADM_API_KEY": clave}, wantErr: "nombre del certificado"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{"ENVIRONMENT", "DOVEADM_API_KEY", "DOVEADM_API_URL", "DOVEADM_API_TLS_SERVER_NAME", "DOVEADM_API_TLS_CA_FILE", "MAIL_HOSTNAME"} {
				t.Setenv(k, tc.env[k])
			}
			c, err := doveadmFromEnv(zap.NewNop())
			switch {
			case tc.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, se esperaba %q", err, tc.wantErr)
				}
			case err != nil:
				t.Fatal(err)
			case (c != nil) != tc.wantClient:
				t.Fatalf("cliente %v, se esperaba %v", c != nil, tc.wantClient)
			}
		})
	}
}
