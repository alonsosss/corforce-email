package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	// DefaultFormMinFill es el tiempo minimo entre servir el formulario y enviarlo
	// (CONTACTS_FORM_MIN_FILL).
	DefaultFormMinFill = 3 * time.Second
	// DefaultFormTokenTTL es cuanto vale el token con que se sirve (CONTACTS_FORM_TOKEN_TTL).
	DefaultFormTokenTTL = 2 * time.Hour
	// MaxFormStatsDays acota la serie de estadisticas de un formulario.
	MaxFormStatsDays = 365
)

// FormConfig son los ajustes de los formularios publicos. PlatformOrigin es el origen de
// PublicBaseURL: el del iframe y el de las paginas de aterrizaje, siempre admitido.
type FormConfig struct {
	MinFill        time.Duration
	TokenTTL       time.Duration
	PlatformOrigin string
}

func (c FormConfig) withDefaults() FormConfig {
	if c.MinFill <= 0 {
		c.MinFill = DefaultFormMinFill
	}
	if c.TokenTTL <= 0 {
		c.TokenTTL = DefaultFormTokenTTL
	}
	return c
}

// ErrFormsUnavailable: el servicio arranco sin lo necesario para los formularios publicos.
var ErrFormsUnavailable = errors.New("los formularios de suscripcion no estan disponibles")

// RateLimitedError es un envio publico por encima del cupo por IP o por formulario.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string {
	return "demasiados envios: vuelve a intentarlo en unos minutos"
}

// FormInput es el alta de un formulario.
type FormInput struct {
	Name           string
	Status         string
	ListID         uuid.UUID
	Fields         []domain.FormField
	Texts          domain.FormTexts
	RedirectURL    *string
	AllowedOrigins []string
}

// FormPatch cambia los campos no nil. RedirectURL con Set y Value nil quita la redireccion.
type FormPatch struct {
	Name           *string
	Status         *string
	ListID         *uuid.UUID
	Fields         *[]domain.FormField
	Texts          *domain.FormTexts
	RedirectURL    *OptionalString
	AllowedOrigins *[]string
}

// OptionalString distingue "quitar" (Value nil) de "no tocar" (el puntero al OptionalString nil).
type OptionalString struct{ Value *string }

func (uc *UseCase) formsReady() error {
	if uc.forms == nil {
		return ErrFormsUnavailable
	}
	return nil
}

// checkFormList comprueba que la lista destino es de la empresa.
func (uc *UseCase) checkFormList(ctx context.Context, tenantID, listID uuid.UUID) error {
	if _, err := uc.lists.Get(ctx, tenantID, listID); err != nil {
		if errors.Is(err, domain.ErrListNotFound) {
			return fmt.Errorf("%w: list_id no es una lista de la empresa", domain.ErrInvalidForm)
		}
		return err
	}
	return nil
}

func (uc *UseCase) CreateForm(ctx context.Context, tenantID, userID uuid.UUID, in FormInput) (*domain.SubscriptionForm, error) {
	if err := uc.formsReady(); err != nil {
		return nil, err
	}
	f := &domain.SubscriptionForm{
		TenantID: tenantID, Name: in.Name, Status: domain.FormStatus(in.Status), ListID: in.ListID,
		Fields: in.Fields, Texts: in.Texts, RedirectURL: in.RedirectURL, AllowedOrigins: in.AllowedOrigins,
		CreatedBy: userID,
	}
	defs, err := uc.definitions(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if err := f.Normalize(defs); err != nil {
		return nil, err
	}
	if err := uc.checkFormList(ctx, tenantID, f.ListID); err != nil {
		return nil, err
	}
	if err := uc.forms.Create(ctx, f); err != nil {
		return nil, err
	}
	return f, nil
}

func (uc *UseCase) UpdateForm(ctx context.Context, tenantID, id uuid.UUID, p FormPatch) (*domain.SubscriptionForm, error) {
	if err := uc.formsReady(); err != nil {
		return nil, err
	}
	f, err := uc.forms.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if p.Name != nil {
		f.Name = *p.Name
	}
	if p.Status != nil {
		f.Status = domain.FormStatus(*p.Status)
	}
	if p.ListID != nil {
		f.ListID = *p.ListID
	}
	if p.Fields != nil {
		f.Fields = *p.Fields
	}
	if p.Texts != nil {
		f.Texts = *p.Texts
	}
	if p.RedirectURL != nil {
		f.RedirectURL = p.RedirectURL.Value
	}
	if p.AllowedOrigins != nil {
		f.AllowedOrigins = *p.AllowedOrigins
	}
	defs, err := uc.definitions(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if err := f.Normalize(defs); err != nil {
		return nil, err
	}
	if p.ListID != nil {
		if err := uc.checkFormList(ctx, tenantID, f.ListID); err != nil {
			return nil, err
		}
	}
	if err := uc.forms.Update(ctx, f); err != nil {
		return nil, err
	}
	return f, nil
}

func (uc *UseCase) GetForm(ctx context.Context, tenantID, id uuid.UUID) (*domain.SubscriptionForm, error) {
	if err := uc.formsReady(); err != nil {
		return nil, err
	}
	return uc.forms.Get(ctx, tenantID, id)
}

func (uc *UseCase) ListForms(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]domain.SubscriptionForm, int64, error) {
	if err := uc.formsReady(); err != nil {
		return nil, 0, err
	}
	return uc.forms.List(ctx, tenantID, page, perPage)
}

func (uc *UseCase) DeleteForm(ctx context.Context, tenantID, id uuid.UUID) error {
	if err := uc.formsReady(); err != nil {
		return err
	}
	return uc.forms.Delete(ctx, tenantID, id)
}

// FormStats cuenta los envios de los ultimos days dias (UTC, hoy incluido).
func (uc *UseCase) FormStats(ctx context.Context, tenantID, id uuid.UUID, days int) (*domain.FormStats, error) {
	if err := uc.formsReady(); err != nil {
		return nil, err
	}
	if days < 1 || days > MaxFormStatsDays {
		return nil, fmt.Errorf("%w: days debe estar entre 1 y %d", domain.ErrInvalidForm, MaxFormStatsDays)
	}
	if _, err := uc.forms.Get(ctx, tenantID, id); err != nil {
		return nil, err
	}
	now := uc.now().UTC()
	to := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	from := to.AddDate(0, 0, -days)
	return uc.forms.Stats(ctx, tenantID, id, from, to)
}

// PublicForm es el formulario activo que se sirve sin sesion, con los atributos declarados con
// que se pintan y se validan sus campos. Uno desactivado o inexistente es ErrFormNotFound.
func (uc *UseCase) PublicForm(ctx context.Context, tenantID, id uuid.UUID) (*domain.SubscriptionForm, domain.Definitions, error) {
	if err := uc.publicFormsReady(); err != nil {
		return nil, nil, err
	}
	f, err := uc.forms.Get(ctx, tenantID, id)
	if err != nil {
		return nil, nil, err
	}
	if !f.Active() {
		return nil, nil, domain.ErrFormNotFound
	}
	defs, err := uc.definitions(ctx, tenantID)
	if err != nil {
		return nil, nil, err
	}
	return f, defs, nil
}

func (uc *UseCase) publicFormsReady() error {
	if uc.forms == nil || uc.formGuard == nil || uc.formTokens == nil {
		return ErrFormsUnavailable
	}
	return nil
}

// FormToken emite el token con que se sirve el formulario ahora.
func (uc *UseCase) FormToken(f *domain.SubscriptionForm) (string, error) {
	if err := uc.publicFormsReady(); err != nil {
		return "", err
	}
	return uc.formTokens.Issue(f.TenantID, f.ID, uc.now())
}

// FormPublicConfig expone al handler el tiempo minimo y la vigencia del token.
func (uc *UseCase) FormPublicConfig() FormConfig { return uc.cfg.Forms }

// CheckSubmitRate cuenta el envio contra el cupo de la IP. Va antes de leer nada: un robot que
// lo agota no llega a la base.
func (uc *UseCase) CheckSubmitRate(ctx context.Context, ip string) error {
	if err := uc.publicFormsReady(); err != nil {
		return err
	}
	if ok, retry := uc.formGuard.AllowIP(ctx, ip); !ok {
		return &RateLimitedError{RetryAfter: retry}
	}
	return nil
}

// SubmitInput es un envio publico ya decodificado. Values lleva cada campo en JSON (el envio por
// fetch) y Strings los del formulario HTML; uno de los dos.
type SubmitInput struct {
	Token     string
	Honeypot  string
	Consent   bool
	Values    map[string]json.RawMessage
	Strings   map[string]string
	IP        string
	UserAgent string
	Origin    string
}

// SubmitResult dice que paso, solo para el registro y las pruebas: la respuesta publica es la
// misma en todos los casos. Ignored es el envio que relleno el campo trampa.
type SubmitResult struct {
	Ignored bool
	Outcome domain.SubmissionOutcome
}

// SubmitForm procesa un envio publico del formulario (ya comprobado su origen): token firmado
// con el tiempo minimo y de un solo uso, campo trampa, cupo por formulario, validacion y
// suppression. Una direccion nueva entra como contacto con el estado que implican sus causas
// vigentes; a una existente no se le cambia ningun dato (cualquiera puede escribir una direccion
// ajena en un formulario publico). Si procede, se pide el doble opt-in con la evidencia del
// formulario; quien confirme entra en la lista destino.
func (uc *UseCase) SubmitForm(ctx context.Context, f *domain.SubscriptionForm, defs domain.Definitions, in SubmitInput) (*SubmitResult, error) {
	if err := uc.publicFormsReady(); err != nil {
		return nil, err
	}
	cfg := uc.cfg.Forms
	nonce, err := uc.formTokens.Verify(in.Token, f.TenantID, f.ID, uc.now(), cfg.MinFill, cfg.TokenTTL)
	if err != nil {
		return nil, err
	}
	if !uc.formGuard.FirstUse(ctx, nonce) {
		return nil, ErrFormTokenInvalid
	}
	if strings.TrimSpace(in.Honeypot) != "" {
		uc.logger.Info("contacts: envio de formulario descartado por el campo trampa",
			zap.String("tenant_id", f.TenantID.String()), zap.String("form_id", f.ID.String()))
		return &SubmitResult{Ignored: true}, nil
	}
	if !in.Consent {
		return nil, domain.ErrConsentNotAccepted
	}
	if ok, retry := uc.formGuard.AllowForm(ctx, f.TenantID, f.ID); !ok {
		return nil, &RateLimitedError{RetryAfter: retry}
	}
	values := in.Values
	if in.Strings != nil {
		if values, err = f.FormValuesFromStrings(in.Strings, defs); err != nil {
			return nil, err
		}
	}
	sub, err := f.ParseSubmission(values, defs)
	if err != nil {
		return nil, err
	}
	candidate := &domain.Contact{
		TenantID: f.TenantID, Email: sub.Email, FirstName: sub.FirstName, LastName: sub.LastName,
		Status: domain.StatusActive, ConsentStatus: domain.ConsentNone, Source: domain.SourceForm,
	}
	if candidate.Attributes, _, err = domain.MergeAttributes(nil, sub.Attributes, defs); err != nil {
		return nil, fmt.Errorf("%w: %w", domain.ErrInvalidSubmission, err)
	}
	if err := domain.CheckRequired(candidate.Attributes, defs); err != nil {
		return nil, fmt.Errorf("%w: %w", domain.ErrInvalidSubmission, err)
	}
	active, err := uc.admissionCauses(ctx, f.TenantID, []string{sub.Email})
	if err != nil {
		return nil, err
	}
	pending := pendingConsent{
		Source:    domain.FormConsentSource(f.ID),
		Evidence:  domain.FormConsentEvidence(f, in.IP, in.Origin),
		UserAgent: domain.NormalizeUserAgent(in.UserAgent),
	}
	var outcome domain.SubmissionOutcome
	submit := func(ctx context.Context) error {
		c, err := uc.contacts.GetByEmailForUpdate(ctx, f.TenantID, sub.Email)
		if errors.Is(err, domain.ErrContactNotFound) {
			fresh := *candidate
			fresh.AdmitSuppression(active[sub.Email])
			if err := uc.contacts.Insert(ctx, &fresh); err != nil {
				return err
			}
			if err := uc.events.ContactCreated(ctx, &fresh); err != nil {
				return err
			}
			c = &fresh
		} else if err != nil {
			return err
		}
		outcome = domain.SubmissionOutcomeFor(c)
		rec := &domain.FormSubmissionRecord{
			TenantID: f.TenantID, FormID: f.ID, ContactID: c.ID, ListID: f.ListID, Outcome: outcome,
		}
		if outcome == domain.OutcomeConfirmationSent {
			req, err := uc.requestConfirmation(ctx, c, pending)
			if err != nil {
				return err
			}
			rec.TokenID = &req.TokenID
		}
		return uc.forms.InsertSubmission(ctx, rec)
	}
	err = uc.tx.Transact(ctx, submit)
	// Dos envios simultaneos de una direccion nueva: el segundo choca con el alta del primero y
	// se repite una vez sobre el contacto ya creado.
	if errors.Is(err, domain.ErrContactExists) {
		err = uc.tx.Transact(ctx, submit)
	}
	if err != nil {
		return nil, err
	}
	return &SubmitResult{Outcome: outcome}, nil
}

// OriginOf reduce una URL (Origin o Referer) a su origen normalizado; vacio si no es http(s).
func OriginOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return u.Scheme + "://" + strings.ToLower(u.Host)
}
