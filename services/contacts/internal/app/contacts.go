package app

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/ports"
	"github.com/google/uuid"
)

// ConsentInput es el consentimiento que una empresa declara por API.
type ConsentInput struct {
	Status    string
	Method    string
	Source    string
	IP        string
	UserAgent string
	Evidence  map[string]any
}

// CreateContactInput es el alta de un contacto por API.
type CreateContactInput struct {
	Email      string
	FirstName  string
	LastName   string
	Locale     string
	Timezone   string
	Attributes domain.RawAttributes
	Tags       []string
	// Source vacio = api. import no se acepta: lo reserva la importacion.
	Source  string
	Consent *ConsentInput
}

// newConsent valida lo declarado y arma la fila de evidencia (sin guardarla).
func (uc *UseCase) newConsent(tenantID, contactID uuid.UUID, in ConsentInput) (*domain.Consent, error) {
	status, err := domain.ParseGrantStatus(in.Status)
	if err != nil {
		return nil, err
	}
	method, err := domain.ParseAPIMethod(in.Method)
	if err != nil {
		return nil, err
	}
	ip, err := domain.NormalizeIP(in.IP)
	if err != nil {
		return nil, err
	}
	evidence := in.Evidence
	if evidence == nil {
		evidence = map[string]any{}
	}
	return &domain.Consent{
		TenantID: tenantID, ContactID: contactID, Purpose: domain.PurposeMarketing,
		Status: status, Method: method, Source: strings.TrimSpace(in.Source),
		IP: ip, UserAgent: domain.NormalizeUserAgent(in.UserAgent), Evidence: evidence,
	}, nil
}

func (uc *UseCase) CreateContact(ctx context.Context, tenantID uuid.UUID, in CreateContactInput) (*domain.Contact, error) {
	email, err := domain.NormalizeEmail(in.Email)
	if err != nil {
		return nil, err
	}
	c := &domain.Contact{TenantID: tenantID, Email: email, Status: domain.StatusActive, ConsentStatus: domain.ConsentNone}
	if c.FirstName, err = domain.NormalizeName(in.FirstName); err != nil {
		return nil, err
	}
	if c.LastName, err = domain.NormalizeName(in.LastName); err != nil {
		return nil, err
	}
	if c.Locale, err = domain.NormalizeLocale(in.Locale); err != nil {
		return nil, err
	}
	if c.Timezone, err = domain.NormalizeTimezone(in.Timezone); err != nil {
		return nil, err
	}
	if c.Tags, err = domain.NormalizeTags(in.Tags); err != nil {
		return nil, err
	}
	switch domain.Source(in.Source) {
	case "":
		c.Source = domain.SourceAPI
	case domain.SourceAPI, domain.SourceForm, domain.SourceIntegration:
		c.Source = domain.Source(in.Source)
	default:
		return nil, domain.ErrInvalidSource
	}
	defs, err := uc.definitions(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if c.Attributes, _, err = domain.MergeAttributes(nil, in.Attributes, defs); err != nil {
		return nil, err
	}
	if err := domain.CheckRequired(c.Attributes, defs); err != nil {
		return nil, err
	}
	var consent *domain.Consent
	if in.Consent != nil {
		if consent, err = uc.newConsent(tenantID, uuid.Nil, *in.Consent); err != nil {
			return nil, err
		}
		// Al crear solo se registra un consentimiento concedido: una revocacion sobre un
		// contacto que nunca consintio no es evidencia de nada.
		if consent.Status != domain.ConsentGranted {
			return nil, domain.ErrInvalidConsentStatus
		}
	}

	err = uc.tx.Transact(ctx, func(ctx context.Context) error {
		if err := uc.contacts.Insert(ctx, c); err != nil {
			return err
		}
		if err := uc.events.ContactCreated(ctx, c); err != nil {
			return err
		}
		if consent == nil {
			return nil
		}
		consent.ContactID = c.ID
		if err := uc.consents.Append(ctx, consent); err != nil {
			return err
		}
		c.ConsentStatus = consent.Status
		return uc.events.ConsentGranted(ctx, consent)
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (uc *UseCase) GetContact(ctx context.Context, tenantID, id uuid.UUID) (*domain.Contact, error) {
	return uc.contacts.GetByID(ctx, tenantID, id)
}

func (uc *UseCase) ListContacts(ctx context.Context, tenantID uuid.UUID, f ports.ContactFilter) ([]domain.Contact, int64, error) {
	if f.Status != "" {
		if _, err := domain.ParseStatus(string(f.Status)); err != nil {
			return nil, 0, err
		}
	}
	if f.Tag != "" {
		tag, err := domain.NormalizeTag(f.Tag)
		if err != nil {
			return nil, 0, err
		}
		f.Tag = tag
	}
	f.Search = strings.ToLower(strings.TrimSpace(f.Search))
	return uc.contacts.List(ctx, tenantID, f)
}

// UpdateContactInput: un puntero nil deja el campo como esta; una cadena vacia en
// locale o timezone lo borra. Attributes se mezcla (null quita la clave); Tags reemplaza.
type UpdateContactInput struct {
	Email      *string
	FirstName  *string
	LastName   *string
	Locale     *string
	Timezone   *string
	Attributes domain.RawAttributes
	Tags       *[]string
}

func (uc *UseCase) UpdateContact(ctx context.Context, tenantID, id uuid.UUID, in UpdateContactInput) (*domain.Contact, error) {
	var defs domain.Definitions
	if len(in.Attributes) > 0 {
		var err error
		if defs, err = uc.definitions(ctx, tenantID); err != nil {
			return nil, err
		}
	}
	var out *domain.Contact
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		c, err := uc.contacts.GetForUpdate(ctx, tenantID, id)
		if err != nil {
			return err
		}
		changed, err := applyUpdate(c, in, defs)
		if err != nil {
			return err
		}
		out = c
		if len(changed) == 0 {
			return nil
		}
		if err := uc.contacts.Update(ctx, c); err != nil {
			return err
		}
		return uc.events.ContactUpdated(ctx, c, changed)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// applyUpdate aplica los cambios sobre el contacto y devuelve los nombres de los campos
// que cambiaron (attributes.<clave> por cada atributo).
func applyUpdate(c *domain.Contact, in UpdateContactInput, defs domain.Definitions) ([]string, error) {
	if in.Email != nil {
		email, err := domain.NormalizeEmail(*in.Email)
		if err != nil || email != c.Email {
			return nil, domain.ErrEmailImmutable
		}
	}
	var changed []string
	if in.FirstName != nil {
		v, err := domain.NormalizeName(*in.FirstName)
		if err != nil {
			return nil, err
		}
		if v != c.FirstName {
			c.FirstName = v
			changed = append(changed, "first_name")
		}
	}
	if in.LastName != nil {
		v, err := domain.NormalizeName(*in.LastName)
		if err != nil {
			return nil, err
		}
		if v != c.LastName {
			c.LastName = v
			changed = append(changed, "last_name")
		}
	}
	if in.Locale != nil {
		v, err := domain.NormalizeLocale(*in.Locale)
		if err != nil {
			return nil, err
		}
		if !sameOptional(c.Locale, v) {
			c.Locale = v
			changed = append(changed, "locale")
		}
	}
	if in.Timezone != nil {
		v, err := domain.NormalizeTimezone(*in.Timezone)
		if err != nil {
			return nil, err
		}
		if !sameOptional(c.Timezone, v) {
			c.Timezone = v
			changed = append(changed, "timezone")
		}
	}
	if in.Tags != nil {
		v, err := domain.NormalizeTags(*in.Tags)
		if err != nil {
			return nil, err
		}
		if !slices.Equal(v, c.Tags) {
			c.Tags = v
			changed = append(changed, "tags")
		}
	}
	if len(in.Attributes) > 0 {
		attrs, keys, err := domain.MergeAttributes(c.Attributes, in.Attributes, defs)
		if err != nil {
			return nil, err
		}
		c.Attributes = attrs
		sort.Strings(keys)
		for _, k := range keys {
			changed = append(changed, "attributes."+k)
		}
	}
	return changed, nil
}

func sameOptional(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// Export es todo lo que la empresa guarda de una persona (derecho de acceso y
// portabilidad): el contacto, sus listas y su historial completo de consentimiento.
type Export struct {
	Contact    *domain.Contact  `json:"contact"`
	Lists      []domain.List    `json:"lists"`
	Consents   []domain.Consent `json:"consents"`
	ExportedAt time.Time        `json:"exported_at"`
}

func (uc *UseCase) ExportContact(ctx context.Context, tenantID, id uuid.UUID) (*Export, error) {
	c, err := uc.contacts.GetByID(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	lists, err := uc.lists.ListsOf(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	consents, err := uc.consents.ListByContact(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if lists == nil {
		lists = []domain.List{}
	}
	if consents == nil {
		consents = []domain.Consent{}
	}
	return &Export{Contact: c, Lists: lists, Consents: consents, ExportedAt: uc.now()}, nil
}

// DeleteContact ejerce el derecho de supresion: borra al contacto con sus membresias y
// enlaces pendientes, y conserva su evidencia de consentimiento seudonimizada (un id sin
// vinculo, sin ip ni user agent, con la huella sha256 de la direccion). La evidencia se
// conserva porque demuestra que los envios pasados fueron legitimos.
func (uc *UseCase) DeleteContact(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		c, err := uc.contacts.GetForUpdate(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if err := uc.contacts.Erase(ctx, tenantID, id, uuid.New(), domain.EmailSHA256(c.Email)); err != nil {
			return fmt.Errorf("borrar contacto: %w", err)
		}
		return uc.events.ContactDeleted(ctx, tenantID, id)
	})
}
