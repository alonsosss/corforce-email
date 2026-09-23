package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/alonsosss/corforce-email/services/templates/internal/deliverability"
	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Valores de ejemplo con los que se renderiza una verificacion. Van en el dominio reservado
// example.com (RFC 2606): no apuntan a nadie y no se confunden con un enlace real.
const (
	sampleUnsubscribeURL   = "https://example.com/unsubscribe/verificacion"
	sampleViewInBrowserURL = "https://example.com/view/verificacion"
	sampleRecipientEmail   = "destinatario@example.com"
	sampleURL              = "https://example.com/"
	sampleString           = "Ejemplo"
)

// DeliverabilityError es el rechazo de una publicacion por la verificacion; lleva el informe.
type DeliverabilityError struct {
	Report deliverability.Report
}

func (e *DeliverabilityError) Error() string { return domain.ErrDeliverabilityFailed.Error() }
func (e *DeliverabilityError) Unwrap() error { return domain.ErrDeliverabilityFailed }

// CheckInput es un contenido que se verifica sin guardarlo. Values son valores de ejemplo de
// las variables; las que falten toman su valor por defecto o uno de ejemplo de su tipo.
type CheckInput struct {
	Kind    string
	Subject string
	HTML    string
	Text    *string
	Values  map[string]json.RawMessage
}

// CheckContent verifica un contenido del editor sin guardarlo. Las variables usadas y no
// declaradas se admiten como cadenas: el panel en vivo no debe fallar mientras se escribe.
func (uc *UseCase) CheckContent(ctx context.Context, tenantID uuid.UUID, in CheckInput) (*deliverability.Report, error) {
	if !contains(domain.Kinds(), in.Kind) {
		return nil, domain.ErrInvalidKind
	}
	compiled, err := uc.renderer.CompileDraft(normalizeContent(domain.Content{Subject: in.Subject, HTML: in.HTML, Text: in.Text}))
	if err != nil {
		return nil, err
	}
	return uc.verify(ctx, tenantID, in.Kind, compiled, in.Values)
}

// CheckVersion verifica una version guardada con los valores por defecto o de ejemplo.
func (uc *UseCase) CheckVersion(ctx context.Context, tenantID, templateID uuid.UUID, version int) (*deliverability.Report, error) {
	t, err := uc.repo.GetTemplate(ctx, tenantID, templateID)
	if err != nil {
		return nil, err
	}
	v, err := uc.repo.GetVersion(ctx, tenantID, templateID, version)
	if err != nil {
		return nil, err
	}
	return uc.checkVersion(ctx, t, v)
}

func (uc *UseCase) checkVersion(ctx context.Context, t *domain.Template, v *domain.Version) (*deliverability.Report, error) {
	compiled, err := uc.renderer.Compile(domain.Content{Subject: v.Subject, HTML: v.HTML, Text: v.Text, Variables: v.Variables})
	if err != nil {
		return nil, err
	}
	return uc.verify(ctx, t.TenantID, t.Kind, compiled, nil)
}

// requireDeliverable impide publicar una version de marketing con errores de verificacion.
// Lo que la transaccion de publicacion rechaza de todos modos (archivada, ya publicada) se deja
// a ella para que el error sea el mismo que sin verificacion.
func (uc *UseCase) requireDeliverable(ctx context.Context, tenantID, templateID uuid.UUID, version int) error {
	t, err := uc.repo.GetTemplate(ctx, tenantID, templateID)
	if err != nil {
		return err
	}
	if t.Kind != domain.KindMarketing || t.Status == domain.TemplateStatusArchived {
		return nil
	}
	v, err := uc.repo.GetVersion(ctx, tenantID, templateID, version)
	if err != nil {
		return err
	}
	if v.Status == domain.VersionStatusPublished {
		return nil
	}
	report, err := uc.checkVersion(ctx, t, v)
	if err != nil {
		return err
	}
	if !report.Passed {
		return &DeliverabilityError{Report: *report}
	}
	return nil
}

func (uc *UseCase) verify(ctx context.Context, tenantID uuid.UUID, kind string, compiled ports.CompiledTemplate, values map[string]json.RawMessage) (*deliverability.Report, error) {
	kit, err := uc.brandKit(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	declared := compiled.Variables()
	resolved, err := domain.ResolveValues(declared, sampleValues(declared, values), map[string]string{
		domain.ReservedUnsubscribeURL:   sampleUnsubscribeURL,
		domain.ReservedViewInBrowserURL: sampleViewInBrowserURL,
		domain.ReservedRecipientEmail:   sampleRecipientEmail,
		domain.ReservedTenantName:       kit.Footer.Company,
	})
	if err != nil {
		return nil, err
	}
	out, err := compiled.Render(resolved)
	if err != nil {
		return nil, err
	}
	marketing := kind == domain.KindMarketing
	spam := deliverability.Unavailable()
	if uc.spam != nil {
		score, err := uc.spam.Check(ctx, ports.SpamSample{
			Marketing: marketing, Subject: out.Subject, HTML: out.HTML, Text: out.Text, UnsubscribeURL: sampleUnsubscribeURL,
		})
		if err != nil {
			uc.logger.Warn("verificacion sin puntuacion antispam", zap.String("tenant_id", tenantID.String()), zap.Error(err))
		} else {
			spam = score
		}
	}
	report := deliverability.Analyze(deliverability.Input{
		Marketing:       marketing,
		Subject:         out.Subject,
		HTML:            out.HTML,
		UnsubscribeURL:  sampleUnsubscribeURL,
		PhysicalAddress: kit.Footer.Address,
		Spam:            spam,
	})
	return &report, nil
}

// sampleValues completa los valores recibidos: una variable sin valor ni default recibe uno de
// ejemplo de su tipo, para que la verificacion vea el correo como saldria.
func sampleValues(declared []domain.Variable, values map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(declared))
	for _, v := range declared {
		if raw, ok := values[v.Name]; ok && string(raw) != "null" {
			out[v.Name] = raw
			continue
		}
		if len(v.Default) > 0 {
			continue
		}
		out[v.Name] = sampleOf(v.Type)
	}
	return out
}

func sampleOf(typ string) json.RawMessage {
	switch typ {
	case domain.VarURL:
		return json.RawMessage(`"` + sampleURL + `"`)
	case domain.VarEmail:
		return json.RawMessage(`"` + sampleRecipientEmail + `"`)
	case domain.VarNumber:
		return json.RawMessage(`0`)
	case domain.VarBoolean:
		return json.RawMessage(`false`)
	default:
		return json.RawMessage(`"` + sampleString + `"`)
	}
}

// IsDeliverabilityError extrae el informe de un rechazo de publicacion.
func IsDeliverabilityError(err error) (*DeliverabilityError, bool) {
	var de *DeliverabilityError
	ok := errors.As(err, &de)
	return de, ok
}
