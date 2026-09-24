package rfc5322

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

func TestFinalizeQuitaElBccYFijaLaFecha(t *testing.T) {
	c := New()
	composed := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	stored, err := c.Compose(domain.Outgoing{
		Draft: domain.Draft{
			From:    domain.Address{Name: "Ana", Email: "ana@empresa.pe"},
			To:      []domain.Address{{Email: "luis@x.pe"}, {Email: "LUIS@x.pe"}},
			Cc:      []domain.Address{{Email: "copia@x.pe"}},
			Bcc:     []domain.Address{{Email: "oculto@x.pe"}},
			Subject: "Informe",
			Text:    "cuerpo del mensaje",
		},
		MessageID: "abc@empresa.pe", Date: composed,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	sent := time.Date(2026, 9, 25, 10, 30, 0, 0, time.UTC)
	final, err := c.Finalize(stored, sent)
	if err != nil {
		t.Fatal(err)
	}
	if final.From != "ana@empresa.pe" || strings.Join(final.Recipients, ",") != "luis@x.pe,copia@x.pe,oculto@x.pe" {
		t.Fatalf("sobre: %+v", final)
	}
	wire, keep := string(final.Wire), string(final.Stored)
	if strings.Contains(wire, "oculto@x.pe") || !strings.Contains(keep, "oculto@x.pe") {
		t.Fatalf("el Bcc solo queda en la copia:\n%s\n---\n%s", wire, keep)
	}
	for _, raw := range []string{wire, keep} {
		if !strings.Contains(raw, "Date: Fri, 25 Sep 2026 10:30:00") || strings.Contains(raw, "24 Sep 2026") {
			t.Fatalf("la fecha es la del envio:\n%s", raw)
		}
		if !strings.Contains(raw, "Message-Id: <abc@empresa.pe>") && !strings.Contains(raw, "Message-ID: <abc@empresa.pe>") {
			t.Fatalf("el Message-ID se conserva:\n%s", raw)
		}
		if !strings.Contains(raw, "cuerpo del mensaje") {
			t.Fatalf("el cuerpo no cambia:\n%s", raw)
		}
		if strings.Contains(strings.ReplaceAll(raw, "\r\n", ""), "\n") {
			t.Fatal("todas las lineas terminan en CRLF")
		}
	}
}

func TestFinalizeRechazaMensajesSinRemitenteValido(t *testing.T) {
	c := New()
	var verr *domain.ValidationError
	for name, raw := range map[string]string{
		"sin cabeceras":    "",
		"sin remitente":    "To: luis@x.pe\r\nSubject: x\r\n\r\ncuerpo",
		"dos remitentes":   "From: a@x.pe, b@x.pe\r\nTo: luis@x.pe\r\n\r\ncuerpo",
		"destino invalido": "From: a@x.pe\r\nTo: no-es-una-direccion\r\n\r\ncuerpo",
	} {
		if _, err := c.Finalize([]byte(raw), time.Now()); !errors.As(err, &verr) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
