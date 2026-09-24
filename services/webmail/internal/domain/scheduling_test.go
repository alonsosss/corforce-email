package domain

import (
	"strings"
	"testing"
)

func TestTextosDeLasInvitaciones(t *testing.T) {
	s := InvitationSummary{Title: "Plan", Start: "2026-10-01T15:00:00Z", End: "2026-10-01T16:30:00Z", TimeZone: "America/Lima",
		Location: "Sala 1", Organizer: "Ana <ana@empresa.pe>"}
	text := InvitationText(InvitationRequest, s)
	for _, want := range []string{"Te invitan a una reunión.", "Asunto: Plan", "Cuándo: 2026-10-01 10:00 - 11:30 (America/Lima)", "Lugar: Sala 1", "Organiza: Ana"} {
		if !strings.Contains(text, want) {
			t.Errorf("falta %q en %q", want, text)
		}
	}
	s.TimeZone = "Luna/Base"
	if !strings.Contains(InvitationText(InvitationCancel, s), "2026-10-01 15:00 - 16:30 (UTC)") {
		t.Fatal("una zona desconocida se escribe en UTC")
	}
	day := InvitationSummary{Title: "Feriado", Start: "2026-10-08T00:00:00Z", End: "2026-10-10T00:00:00Z", AllDay: true}
	if booking := InvitationText(InvitationBooking, day); !strings.HasPrefix(booking, "La cita está confirmada.") || !strings.Contains(booking, "Cuándo: 2026-10-08 - 2026-10-09") {
		t.Fatalf("dia completo: %q", InvitationText(InvitationBooking, day))
	}
	if InvitationSubject(ReplyKind(PartStatTentative), "Plan") != "Tentativa: Plan" || InvitationSubject(ReplyKind(PartStatDeclined), "x") != "Rechazada: x" {
		t.Fatal("asuntos de respuesta")
	}
	if got := InvitationSubject(InvitationRequest, strings.Repeat("a", 2000)); len([]rune(got)) != MaxSubjectRunes {
		t.Fatalf("asunto acotado: %d", len([]rune(got)))
	}
	m := InvitationMail{To: []Address{{Email: "a@x.pe"}, {Email: "B@x.pe"}}, Cc: []Address{{Email: "A@x.pe"}}}
	if r := m.Recipients(); len(r) != 2 {
		t.Fatalf("destinatarios sin repetir: %v", r)
	}
}

func TestEntradasDeLaPlanificacion(t *testing.T) {
	if r, err := ValidateInvitationResponse(" tentative "); err != nil || r != PartStatTentative {
		t.Fatal("respuesta")
	}
	if _, err := ValidateInvitationResponse("NEEDS-ACTION"); err == nil {
		t.Fatal("NEEDS-ACTION no es una respuesta")
	}
	if !IsCalendarPart("Text/Calendar") || !IsCalendarPart("application/ics") || IsCalendarPart("text/plain") {
		t.Fatal("IsCalendarPart")
	}
	if list, err := NewAvailabilityQuery([]string{"a@x.pe, B@x.pe", "a@x.pe"}); err != nil || len(list) != 2 || list[1] != "b@x.pe" {
		t.Fatalf("direcciones: %v %v", list, err)
	}
	if _, err := NewAvailabilityQuery([]string{" , "}); err == nil {
		t.Fatal("sin direcciones")
	}
	many := make([]string, MaxAvailabilityAddresses+1)
	for i := range many {
		many[i] = strings.Repeat("a", i+1) + "@x.pe"
	}
	if _, err := NewAvailabilityQuery(many); err == nil {
		t.Fatal("demasiadas direcciones")
	}
	tenant := "11111111-1111-4111-8111-111111111111"
	if !ValidPublicBookingTarget("pe-01", tenant, "abcdefghijklmnopqrstuvwxyz") || ValidPublicBookingTarget("pe-01", tenant, "../..") ||
		ValidPublicBookingTarget("PE 01", tenant, "abcdefghijklmnopqrstuvwxyz") || ValidPublicBookingTarget("pe-01", "x", "abcdefghijklmnopqrstuvwxyz") {
		t.Fatal("ValidPublicBookingTarget")
	}
}
