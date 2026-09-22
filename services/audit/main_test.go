package main

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// anchorEnv deja el entorno del informe de anclas como lo daria un .env completo, y cada prueba
// quita o cambia lo que quiere probar.
func anchorEnv(t *testing.T) uuid.UUID {
	t.Helper()
	platform := uuid.New()
	t.Setenv("AUDIT_ANCHOR_RUA", "")
	t.Setenv("AUDIT_ANCHOR_REPORT_INTERVAL", "")
	t.Setenv("TRANSACTIONAL_URL", "http://transactional:8045")
	t.Setenv("PLATFORM_TENANT_ID", platform.String())
	t.Setenv("INTERNAL_GATEWAY_TOKEN", "token-interno")
	return platform
}

func TestSinDireccionesElInformeDeAnclasQuedaDesactivadoYNadaMasSeExige(t *testing.T) {
	anchorEnv(t)
	t.Setenv("TRANSACTIONAL_URL", "")
	t.Setenv("PLATFORM_TENANT_ID", "")
	t.Setenv("INTERNAL_GATEWAY_TOKEN", "")
	st, err := loadAnchorReportSettings()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.recipients) != 0 || st.every != defaultAnchorReportInterval {
		t.Fatalf("%+v", st)
	}
	// Solo comas y espacios tampoco es una direccion.
	t.Setenv("AUDIT_ANCHOR_RUA", " , ,")
	if st, err := loadAnchorReportSettings(); err != nil || len(st.recipients) != 0 {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestLasDireccionesSeNormalizanYNoSeRepiten(t *testing.T) {
	platform := anchorEnv(t)
	t.Setenv("AUDIT_ANCHOR_RUA", " Anclas@Example.org, archivo@example.net ,anclas@example.org")
	t.Setenv("AUDIT_ANCHOR_REPORT_INTERVAL", "6h")
	st, err := loadAnchorReportSettings()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(st.recipients, ",") != "anclas@example.org,archivo@example.net" {
		t.Fatalf("direcciones: %v", st.recipients)
	}
	if st.every != 6*time.Hour || st.transactionalURL != "http://transactional:8045" || st.internalToken != "token-interno" || st.platformTenant != platform {
		t.Fatalf("%+v", st)
	}
}

func TestUnaDireccionInvalidaNoArranca(t *testing.T) {
	anchorEnv(t)
	for _, bad := range []string{"anclas", "anclas@", "@example.org", "anclas@example", "dos direcciones@example.org"} {
		t.Setenv("AUDIT_ANCHOR_RUA", "buena@example.org,"+bad)
		if _, err := loadAnchorReportSettings(); err == nil || !strings.Contains(err.Error(), "AUDIT_ANCHOR_RUA") {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

func TestElIntervaloDelInformeTieneRango(t *testing.T) {
	anchorEnv(t)
	t.Setenv("AUDIT_ANCHOR_RUA", "anclas@example.org")
	for _, bad := range []string{"30m", "8d", "0", "ayer"} {
		t.Setenv("AUDIT_ANCHOR_REPORT_INTERVAL", bad)
		if _, err := loadAnchorReportSettings(); err == nil || !strings.Contains(err.Error(), "AUDIT_ANCHOR_REPORT_INTERVAL") {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	for _, good := range []string{"1h", "168h", "24h"} {
		t.Setenv("AUDIT_ANCHOR_REPORT_INTERVAL", good)
		if _, err := loadAnchorReportSettings(); err != nil {
			t.Fatalf("%q: %v", good, err)
		}
	}
}

// Con direcciones puestas, lo que el correo necesita para salir es obligatorio: un informe
// configurado que no puede enviarse es un error de configuracion, no un aviso en el log.
func TestConDireccionesElCorreoDePlataformaEsObligatorio(t *testing.T) {
	casos := map[string]struct {
		variable, valor, mensaje string
	}{
		"sin transactional":         {"TRANSACTIONAL_URL", "", "TRANSACTIONAL_URL"},
		"transactional mal formado": {"TRANSACTIONAL_URL", "transactional:8045/x", "TRANSACTIONAL_URL"},
		"sin empresa de plataforma": {"PLATFORM_TENANT_ID", "", "PLATFORM_TENANT_ID"},
		"empresa que no es un uuid": {"PLATFORM_TENANT_ID", "platform", "PLATFORM_TENANT_ID"},
	}
	for name, c := range casos {
		t.Run(name, func(t *testing.T) {
			anchorEnv(t)
			t.Setenv("AUDIT_ANCHOR_RUA", "anclas@example.org")
			t.Setenv(c.variable, c.valor)
			_, err := loadAnchorReportSettings()
			if err == nil || !strings.Contains(err.Error(), c.mensaje) {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestElTokenInternoSeExigeFueraDeDesarrollo(t *testing.T) {
	anchorEnv(t)
	t.Setenv("AUDIT_ANCHOR_RUA", "anclas@example.org")
	t.Setenv("INTERNAL_GATEWAY_TOKEN", "")
	t.Setenv("ENVIRONMENT", "production")
	if _, err := loadAnchorReportSettings(); err == nil {
		t.Fatal("en produccion el informe no puede salir sin el token interno")
	}
}
