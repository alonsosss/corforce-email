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
	sampleImageURL         = "https://example.com/imagen.png"
	sampleString           = "Ejemplo"
	// sampleListItems es cuantos elementos lleva una lista de ejemplo en una vista previa o un
	// envio de prueba. La verificacion usa en cambio el maximo que la plantilla puede mostrar.
	sampleListItems = 3
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
	// Cada lista se mide con el maximo que la plantilla puede mostrar: html_too_large tiene
	// que ver el peor caso frente al recorte de Gmail, no una muestra de tres productos.
	worstCase := func(name string) int {
		if n := compiled.ListCap(name); n > 0 {
			return n
		}
		return sampleListItems
	}
	resolved, err := domain.ResolveValues(declared, sampleValues(declared, values, worstCase), map[string]string{
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
		UnboundedLists:  compiled.UnboundedLists(),
	})
	return &report, nil
}

// sampleValues completa los valores recibidos: una variable sin valor ni default recibe uno de
// ejemplo de su tipo, para que la verificacion vea el correo como saldria. items dice cuantos
// elementos lleva cada lista de ejemplo.
func sampleValues(declared []domain.Variable, values map[string]json.RawMessage, items func(name string) int) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(declared))
	for _, v := range declared {
		if raw, ok := values[v.Name]; ok && string(raw) != "null" {
			out[v.Name] = raw
			continue
		}
		if len(v.Default) > 0 {
			continue
		}
		if v.Type == domain.VarList {
			out[v.Name] = sampleList(v.Fields, items(v.Name))
			continue
		}
		out[v.Name] = sampleOf(v.Type)
	}
	return out
}

func sampleList(fields []domain.Field, n int) json.RawMessage {
	item := make(map[string]json.RawMessage, len(fields))
	for _, f := range fields {
		item[f.Name] = sampleOf(f.Type)
	}
	list := make([]map[string]json.RawMessage, n)
	for i := range list {
		list[i] = item
	}
	b, err := json.Marshal(list)
	if err != nil {
		return json.RawMessage(`[]`)
	}
	return b
}

// fixedSampleCount es el numero de elementos de las listas de una vista previa o una prueba.
func fixedSampleCount(string) int { return sampleListItems }

func sampleOf(typ string) json.RawMessage {
	switch typ {
	case domain.VarImage:
		return json.RawMessage(`"` + sampleImageURL + `"`)
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
