package maildavcli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// El contrato de la planificacion con mail-dav: rutas, cabeceras de identidad (con la direccion del buzon solo si
// es una direccion) y rechazos que llegan tal cual.
func TestPlanificacionContraMailDav(t *testing.T) {
	withAddress := mailbox
	withAddress.Address = "Ana@Empresa.pe"
	var bodies = map[string]map[string]any{}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		route := r.Method + " " + r.URL.EscapedPath()
		if r.Body != nil {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			bodies[route] = b
		}
		switch route {
		case "POST /internal/mail-dav/availability":
			checkIdentity(t, r)
			if r.Header.Get("X-Mailbox-Address") != "ana@empresa.pe" {
				t.Errorf("direccion del buzon: %q", r.Header.Get("X-Mailbox-Address"))
			}
			_, _ = w.Write([]byte(`{"data":[{"address":"bea@empresa.pe","known":true,"partial":false,"busy":[{"start":"2026-10-01T15:00:00Z","end":"2026-10-01T16:00:00Z"}]}]}`))
		case "PUT /internal/mail-dav/calendar/events/e1/occurrences/2026-10-02T15:00:00Z":
			if r.Header.Get("If-Match") != `"v1"` {
				t.Errorf("If-Match: %q", r.Header.Get("If-Match"))
			}
			_, _ = w.Write([]byte(`{"data":{"id":"e1","etag":"\"v2\"","title":"x","attendees":[{"email":"bea@empresa.pe","name":"","partstat":"ACCEPTED"}],"organizer":{"email":"ana@empresa.pe","name":"Ana"}}}`))
		case "POST /internal/mail-dav/itip/respond":
			_, _ = w.Write([]byte(`{"data":{"reply":"BEGIN:VCALENDAR","organizer":{"email":"jefe@otra.pe","name":"Jefe"},"attendee":"ana@empresa.pe","event_id":"ev"}}`))
		case "POST /internal/mail-dav/booking/public/11111111-1111-4111-8111-111111111111/enlace-publico-de-prueba-01/reservations":
			if r.Header.Get("X-Mailbox-ID") != "" {
				t.Errorf("la reserva publica no lleva buzon: %v", r.Header)
			}
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"SLOT_UNAVAILABLE","message":"el hueco ya no esta disponible"}}`))
		default:
			t.Errorf("ruta inesperada %s", route)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ctx := context.Background()
	w, _ := domain.NewEventWindow("2026-10-01T00:00:00Z", "2026-10-08T00:00:00Z")
	list, err := c.Availability(ctx, withAddress, []string{"bea@empresa.pe"}, w)
	if err != nil || len(list) != 1 || !list[0].Known || list[0].Busy[0].End != "2026-10-01T16:00:00Z" {
		t.Fatalf("disponibilidad: %+v %v", list, err)
	}
	if b := bodies["POST /internal/mail-dav/availability"]; b["start"] != "2026-10-01T00:00:00Z" || len(b["addresses"].([]any)) != 1 {
		t.Fatalf("cuerpo: %v", b)
	}
	e, err := c.UpdateOccurrence(ctx, mailbox, "e1", "2026-10-02T15:00:00Z", domain.EventInput{Title: "x"}, `"v1"`)
	if err != nil || e.ETag != `"v2"` || e.Organizer == nil || e.Attendees[0].PartStat != "ACCEPTED" {
		t.Fatalf("aparicion: %+v %v", e, err)
	}
	ans, err := c.RespondInvitation(ctx, mailbox, "BEGIN:VCALENDAR", []string{"ana@empresa.pe"}, "ACCEPTED")
	if err != nil || ans.Organizer.Email != "jefe@otra.pe" || ans.EventID != "ev" {
		t.Fatalf("respuesta: %+v %v", ans, err)
	}
	if b := bodies["POST /internal/mail-dav/itip/respond"]; b["response"] != "ACCEPTED" || b["ical"] != "BEGIN:VCALENDAR" {
		t.Fatalf("cuerpo: %v", b)
	}
	_, err = c.Book(ctx, "11111111-1111-4111-8111-111111111111", "enlace-publico-de-prueba-01", domain.BookingRequest{Start: "2026-10-01T15:00:00Z"})
	var rejection *domain.ServiceRejection
	if !errors.As(err, &rejection) || rejection.Kind != domain.RejectConflict || rejection.Code != "SLOT_UNAVAILABLE" {
		t.Fatalf("hueco ocupado: %v", err)
	}
}
