package app

import (
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
)

// El asunto del lote (variante A/B o reenvio de campaigns) sustituye al de la plantilla en
// todos los mensajes y queda guardado en ellos; sin asunto manda el de la plantilla.
func TestMarketingBatchSubjectOverride(t *testing.T) {
	f := newMarketingFixture(t)
	cmd := batchCommand(f, "ana@example.com", "eva@example.com")
	cmd.Subject = "  Ultimo dia de la oferta  "
	res, err := f.uc.CreateMarketingBatch(ctx, cmd)
	if err != nil || res.Accepted != 2 {
		t.Fatalf("lote: %+v, %v", res, err)
	}
	for _, id := range res.MessageIDs {
		if got := f.repo.messages[id].Subject; got != "Ultimo dia de la oferta" {
			t.Fatalf("asunto del mensaje %s = %q", id, got)
		}
	}

	f = newMarketingFixture(t)
	cmd = batchCommand(f, "ana@example.com")
	res, err = f.uc.CreateMarketingBatch(ctx, cmd)
	if err != nil || res.Accepted != 1 {
		t.Fatalf("lote sin asunto: %+v, %v", res, err)
	}
	if got := f.repo.messages[res.MessageIDs[0]].Subject; got != "Hola ana@example.com" {
		t.Fatalf("sin asunto propio manda el de la plantilla: %q", got)
	}
}

func TestMarketingBatchSubjectValidation(t *testing.T) {
	for name, subject := range map[string]string{
		"salto de linea":      "Oferta\r\nBcc: x@example.com",
		"caracter de control": "Oferta\x07",
		"demasiado largo":     strings.Repeat("a", MaxSubjectOverride+1),
	} {
		f := newMarketingFixture(t)
		cmd := batchCommand(f, "ana@example.com")
		cmd.Subject = subject
		if _, err := f.uc.CreateMarketingBatch(ctx, cmd); !domain.IsValidation(err) {
			t.Errorf("%s: se esperaba error de validacion, err = %v", name, err)
		}
		nothingCreated(t, f, name)
	}
	f := newMarketingFixture(t)
	cmd := batchCommand(f, "ana@example.com")
	cmd.Subject = strings.Repeat("n", MaxSubjectOverride)
	if _, err := f.uc.CreateMarketingBatch(ctx, cmd); err != nil {
		t.Fatalf("un asunto en el tope es valido: %v", err)
	}
}
