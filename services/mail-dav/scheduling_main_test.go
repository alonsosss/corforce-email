package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app"
	"go.uber.org/zap"
)

var schedulingKeys = []string{
	"MAIL_DAV_BUSY_HORIZON_DAYS", "MAIL_DAV_BUSY_LOOKBACK_DAYS", "MAIL_DAV_MAX_BUSY_PER_EVENT", "MAIL_DAV_BUSY_REFRESH_MAX",
	"MAIL_DAV_AVAILABILITY_MAX_ADDRESSES", "MAIL_DAV_BOOKING_MAX_DAILY", "MAIL_DAV_BOOKING_MAX_PER_VISITOR",
	"MAIL_DAV_BOOKING_MAX_SLOTS", "MAIL_DAV_BOOKING_MAX_WINDOW_DAYS",
}

func setSchedulingEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	base := map[string]string{"MAIL_AUTH_URL": "https://mail-auth:9082"}
	for _, k := range schedulingKeys {
		base[k] = ""
	}
	for k, v := range kv {
		base[k] = v
	}
	setEnv(t, base)
}

// Sin configuracion nueva la planificacion arranca con los valores del ADR, y el cuerpo de una invitacion admite
// dos veces el tope de un evento.
func TestLaPlanificacionArrancaConLosValoresDelADR(t *testing.T) {
	setSchedulingEnv(t, nil)
	st, err := loadSettings(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	want := app.SchedulingConfig{
		BusyHorizon: 400 * 24 * time.Hour, BusyLookback: 7 * 24 * time.Hour, MaxBusyPerEvent: 1000, BusyRefreshMax: 500,
		MaxAvailabilityAddresses: 20, BookingMaxDaily: 50, BookingMaxPerVisitor: 2, BookingMaxSlots: 300,
		BookingMaxWindow: 31 * 24 * time.Hour,
	}
	if st.app.Scheduling != want {
		t.Fatalf("planificacion: %+v", st.app.Scheduling)
	}
	if st.api.MaxITIPBodyBytes != 2*int64(st.app.Calendar.MaxEventBytes)+multipartSlack {
		t.Fatalf("cuerpo de una invitacion: %d", st.api.MaxITIPBodyBytes)
	}
}

func TestLaPlanificacionSeApagaConHorizonteCero(t *testing.T) {
	setSchedulingEnv(t, map[string]string{"MAIL_DAV_BUSY_HORIZON_DAYS": "0"})
	st, err := loadSettings(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if st.app.Scheduling.BusyHorizon != 0 {
		t.Fatalf("horizonte: %v", st.app.Scheduling.BusyHorizon)
	}
}

func TestLaPlanificacionInvalidaImpideArrancar(t *testing.T) {
	for key, value := range map[string]string{
		"MAIL_DAV_BUSY_HORIZON_DAYS":          "-1",
		"MAIL_DAV_BUSY_LOOKBACK_DAYS":         "0",
		"MAIL_DAV_AVAILABILITY_MAX_ADDRESSES": "51",
		"MAIL_DAV_BOOKING_MAX_WINDOW_DAYS":    "63",
		"MAIL_DAV_BOOKING_MAX_DAILY":          "cero",
		"MAIL_DAV_MAX_BUSY_PER_EVENT":         "0",
		"MAIL_DAV_BOOKING_MAX_PER_VISITOR":    "101",
		"MAIL_DAV_BOOKING_MAX_SLOTS":          "5001",
		"MAIL_DAV_BUSY_REFRESH_MAX":           "10001",
	} {
		t.Run(key, func(t *testing.T) {
			setSchedulingEnv(t, map[string]string{key: value})
			if _, err := loadSettings(zap.NewNop()); err == nil {
				t.Fatalf("%s=%s arranco", key, value)
			}
		})
	}
}

func TestLaEmpresaDeUnaRutaPublicaDeCitas(t *testing.T) {
	for path, want := range map[string]string{
		"/internal/mail-dav/booking/public/11111111-1111-4111-8111-111111111111/pagina":              "11111111-1111-4111-8111-111111111111",
		"/internal/mail-dav/booking/public/11111111-1111-4111-8111-111111111111/pagina/reservations": "11111111-1111-4111-8111-111111111111",
		"/internal/mail-dav/booking/public/no-uuid/pagina":                                           "",
		"/internal/mail-dav/booking":                                                                 "",
		"/internal/mail-dav/contacts":                                                                "",
	} {
		got, ok := publicBookingTenant(path)
		if got != want || ok != (want != "") {
			t.Errorf("%s: %q %v", path, got, ok)
		}
	}
}

// La pagina publica no lleva buzon: su cupo es por empresa, y el trafico anonimo de una no gasta el de otra ni el
// de los buzones.
func TestLaPaginaPublicaDeCitasTieneCupoPorEmpresa(t *testing.T) {
	t.Setenv("INTERNAL_GATEWAY_TOKEN", "token-interno")
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	dav := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := router(dav, api, settings{ratePerMin: 1, maxInflight: 8, reqTimeout: time.Minute}, zap.NewNop())
	call := func(path, mailbox string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Gateway-Token", "token-interno")
		if mailbox != "" {
			req.Header.Set("X-Mailbox-ID", mailbox)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	tenantA := "/internal/mail-dav/booking/public/11111111-1111-4111-8111-111111111111/pagina"
	tenantB := "/internal/mail-dav/booking/public/22222222-2222-4222-8222-222222222222/pagina"
	if got := call(tenantA, ""); got != http.StatusOK {
		t.Fatalf("primera: %d", got)
	}
	if got := call(tenantA, ""); got != http.StatusTooManyRequests {
		t.Fatalf("pasado el cupo de la empresa: %d", got)
	}
	if got := call(tenantB, ""); got != http.StatusOK {
		t.Fatalf("otra empresa tiene su cupo: %d", got)
	}
	if got := call("/internal/mail-dav/contacts", "6f1c1f8e-3f55-4a55-9f39-1f8a2f0b7a11"); got != http.StatusOK {
		t.Fatalf("un buzon no gasta el cupo de las citas: %d", got)
	}
}
