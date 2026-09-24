package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

const (
	// tokenBytes es la entropia del enlace de confirmacion (256 bits).
	tokenBytes = 32
	// ConfirmPath es la ruta publica (por el gateway) que confirma el doble opt-in.
	ConfirmPath = "/api/v1/public/contacts/confirm"
)

// RecordConsent registra un consentimiento declarado por la empresa (method api o form).
// Revocar siempre se acepta. Conceder a quien se dio de baja o retiro su consentimiento
// exige un formulario con ip (domain.CheckGrant); si procede, el contacto vuelve a active
// y suppression levanta la baja al recibir contacts.contact.resubscribed.
func (uc *UseCase) RecordConsent(ctx context.Context, tenantID, contactID uuid.UUID, in ConsentInput) (*domain.Consent, error) {
	consent, err := uc.newConsent(tenantID, contactID, in)
	if err != nil {
		return nil, err
	}
	err = uc.tx.Transact(ctx, func(ctx context.Context) error {
		c, err := uc.contacts.GetForUpdate(ctx, tenantID, contactID)
		if err != nil {
			return err
		}
		if consent.Status == domain.ConsentGranted {
			if err := domain.CheckGrant(c, consent.Method, consent.IP); err != nil {
				return err
			}
		}
		return uc.appendConsent(ctx, c, consent)
	})
	if err != nil {
		return nil, err
	}
	return consent, nil
}

// appendConsent guarda la evidencia sobre un contacto YA bloqueado, publica su evento y,
// si es una concesion que reactiva a quien se dio de baja, lo reactiva. La resuscripcion
// lleva el occurred_at que Append acaba de leer de la base: es la hora con que suppression
// decide que bajas levanta este consentimiento.
func (uc *UseCase) appendConsent(ctx context.Context, c *domain.Contact, consent *domain.Consent) error {
	if err := uc.consents.Append(ctx, consent); err != nil {
		return err
	}
	c.ConsentStatus = consent.Status
	switch consent.Status {
	case domain.ConsentRevoked:
		return uc.events.ConsentRevoked(ctx, consent)
	case domain.ConsentGranted:
		if c.Reactivate() {
			if err := uc.contacts.Update(ctx, c); err != nil {
				return err
			}
			if err := uc.events.ContactUpdated(ctx, c, []string{"status"}); err != nil {
				return err
			}
			if err := uc.events.ContactResubscribed(ctx, c, consent.OccurredAt); err != nil {
				return err
			}
		}
		return uc.events.ConsentGranted(ctx, consent)
	}
	return nil
}

func (uc *UseCase) ListConsents(ctx context.Context, tenantID, contactID uuid.UUID) ([]domain.Consent, error) {
	if _, err := uc.contacts.GetByID(ctx, tenantID, contactID); err != nil {
		return nil, err
	}
	return uc.consents.ListByContact(ctx, tenantID, contactID)
}

// ConfirmationRequest es lo que devuelve la peticion de doble opt-in. El enlace NO se
// devuelve a la empresa: si pudiera leerlo podria confirmar en nombre de la persona. Viaja
// solo en contacts.consent.requested, que consume automations para enviar el correo.
type ConfirmationRequest struct {
	ContactID uuid.UUID            `json:"contact_id"`
	Status    domain.ConsentStatus `json:"status"`
	ExpiresAt time.Time            `json:"expires_at"`
	// TokenID es el enlace emitido; lo guarda el envio de un formulario para reconocer su
	// confirmacion.
	TokenID uuid.UUID `json:"-"`
}

// pendingConsent es lo que queda como evidencia de la peticion: su origen y, si la pidio un
// formulario, el texto aceptado, la ip truncada y el user agent.
type pendingConsent struct {
	Source    string
	Evidence  map[string]any
	UserAgent *string
}

// RequestConfirmation inicia el doble opt-in: fila pending, token de un solo uso (solo se
// guarda su sha256) y evento con el enlace. Un enlace anterior sin usar deja de valer.
func (uc *UseCase) RequestConfirmation(ctx context.Context, tenantID, contactID uuid.UUID, source string) (*ConfirmationRequest, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		source = "api"
	}
	var out *ConfirmationRequest
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		c, err := uc.contacts.GetForUpdate(ctx, tenantID, contactID)
		if err != nil {
			return err
		}
		out, err = uc.requestConfirmation(ctx, c, pendingConsent{Source: source})
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// requestConfirmation pide el doble opt-in de un contacto YA bloqueado en la transaccion.
func (uc *UseCase) requestConfirmation(ctx context.Context, c *domain.Contact, p pendingConsent) (*ConfirmationRequest, error) {
	if err := domain.CheckConfirmationRequest(c); err != nil {
		return nil, err
	}
	raw := make([]byte, tokenBytes)
	if _, err := io.ReadFull(uc.random, raw); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	out := &ConfirmationRequest{ContactID: c.ID, Status: domain.ConsentPending, ExpiresAt: uc.now().Add(uc.cfg.DOITTL)}
	if err := uc.tokens.DeleteUnused(ctx, c.TenantID, c.ID); err != nil {
		return nil, err
	}
	t := &domain.ConfirmationToken{TenantID: c.TenantID, ContactID: c.ID, TokenHash: hashToken(raw), ExpiresAt: out.ExpiresAt}
	if err := uc.tokens.Create(ctx, t); err != nil {
		return nil, err
	}
	out.TokenID = t.ID
	evidence := make(map[string]any, len(p.Evidence)+1)
	for k, v := range p.Evidence {
		evidence[k] = v
	}
	evidence["token_id"] = t.ID.String()
	pending := &domain.Consent{
		TenantID: c.TenantID, ContactID: c.ID, Purpose: domain.PurposeMarketing,
		Status: domain.ConsentPending, Method: domain.MethodDoubleOptIn, Source: p.Source,
		UserAgent: p.UserAgent, Evidence: evidence,
	}
	if err := uc.consents.Append(ctx, pending); err != nil {
		return nil, err
	}
	c.ConsentStatus = domain.ConsentPending
	if err := uc.events.ConsentRequested(ctx, c, uc.confirmURL(c.TenantID, token)); err != nil {
		return nil, err
	}
	return out, nil
}

func (uc *UseCase) confirmURL(tenantID uuid.UUID, token string) string {
	q := url.Values{}
	q.Set("t", tenantID.String())
	q.Set("k", token)
	return uc.cfg.PublicBaseURL + ConfirmPath + "?" + q.Encode()
}

// hashToken es la huella que se guarda: sha256 de los 32 bytes del token, en hex.
func hashToken(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// parseToken decodifica el token del enlace. Cualquier forma distinta de 32 bytes en
// base64url sin relleno es un enlace no valido, sin distinguir por que. Strict rechaza
// ademas los bits de relleno distintos de cero: un token tiene una sola escritura.
func parseToken(k string) (string, error) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimSpace(k))
	if err != nil || len(raw) != tokenBytes {
		return "", domain.ErrInvalidConfirmation
	}
	return hashToken(raw), nil
}

// CheckConfirmation comprueba, sin cambiar nada, que el enlace aun confirma. La pagina
// GET la usa para no ofrecer un boton que va a fallar.
func (uc *UseCase) CheckConfirmation(ctx context.Context, tenantID uuid.UUID, k string) error {
	hash, err := parseToken(k)
	if err != nil {
		return err
	}
	t, err := uc.tokens.GetByHash(ctx, tenantID, hash)
	if err != nil {
		return err
	}
	if !t.Usable(uc.now()) {
		return domain.ErrInvalidConfirmation
	}
	return nil
}

// Confirm cierra el doble opt-in: consume el token, guarda el consentimiento concedido
// con la ip y el user agent de quien pulso el boton, y reactiva a quien se habia dado de
// baja. Todo fallo del enlace es domain.ErrInvalidConfirmation.
func (uc *UseCase) Confirm(ctx context.Context, tenantID uuid.UUID, k, ip, userAgent string) error {
	hash, err := parseToken(k)
	if err != nil {
		return err
	}
	clientIP, err := domain.NormalizeIP(ip)
	if err != nil {
		clientIP = nil
	}
	now := uc.now()
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		t, err := uc.tokens.GetByHashForUpdate(ctx, tenantID, hash)
		if err != nil {
			return err
		}
		if !t.Usable(now) {
			return domain.ErrInvalidConfirmation
		}
		c, err := uc.contacts.GetForUpdate(ctx, tenantID, t.ContactID)
		if errors.Is(err, domain.ErrContactNotFound) {
			return domain.ErrInvalidConfirmation
		}
		if err != nil {
			return err
		}
		if err := uc.tokens.MarkUsed(ctx, tenantID, t.ID, now); err != nil {
			return err
		}
		if err := uc.joinFormList(ctx, tenantID, t.ID, c.ID, now); err != nil {
			return err
		}
		granted := &domain.Consent{
			TenantID: tenantID, ContactID: c.ID, Purpose: domain.PurposeMarketing,
			Status: domain.ConsentGranted, Method: domain.MethodDoubleOptIn,
			Source: "confirmation_token:" + t.ID.String(), IP: clientIP,
			UserAgent: domain.NormalizeUserAgent(userAgent),
			Evidence:  map[string]any{"token_id": t.ID.String(), "requested_at": t.CreatedAt.UTC().Format(time.RFC3339)},
		}
		return uc.appendConsent(ctx, c, granted)
	})
}

// joinFormList cierra el envio de formulario que pidio el token, si lo hubo: lo marca
// confirmado y pone al contacto en la lista destino del formulario.
func (uc *UseCase) joinFormList(ctx context.Context, tenantID, tokenID, contactID uuid.UUID, at time.Time) error {
	if uc.forms == nil {
		return nil
	}
	listID, err := uc.forms.ConfirmSubmission(ctx, tenantID, tokenID, at)
	if err != nil || listID == nil {
		return err
	}
	_, err = uc.lists.AddMembers(ctx, tenantID, *listID, []uuid.UUID{contactID})
	return err
}
