package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	kindTransactional = "transactional"
	kindMarketing     = "marketing"
	// doiProbePath es la ruta del enlace de prueba con que se comprueba que la plantilla
	// del doble opt-in muestra confirm_url. No se sirve: nunca sale en un correo.
	doiProbePath = "/automations/doi-template-check/"
)

// GetDOISettings devuelve los ajustes; sin fila, unos desactivados y vacios.
func (uc *UseCase) GetDOISettings(ctx context.Context, tenantID uuid.UUID) (*domain.DOISettings, error) {
	s, err := uc.settings.GetDOISettings(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if s == nil {
		s = &domain.DOISettings{TenantID: tenantID}
	}
	return s, nil
}

// UpdateDOISettings valida y guarda. Si hay plantilla, se renderiza en templates con un
// enlace de prueba: debe ser transaccional y mostrar confirm_url.
func (uc *UseCase) UpdateDOISettings(ctx context.Context, tenantID uuid.UUID, in domain.DOISettings, updatedBy *uuid.UUID) (*domain.DOISettings, error) {
	s := in
	s.TenantID = tenantID
	if err := s.Normalize(); err != nil {
		return nil, err
	}
	if s.TemplateID != nil {
		if err := uc.checkDOITemplate(ctx, tenantID, *s.TemplateID); err != nil {
			return nil, err
		}
	}
	s.UpdatedBy = updatedBy
	if err := uc.settings.UpsertDOISettings(ctx, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (uc *UseCase) checkDOITemplate(ctx context.Context, tenantID, templateID uuid.UUID) error {
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	probe := uc.cfg.PublicBaseURL + doiProbePath + hex.EncodeToString(nonce)
	probeJSON, _ := json.Marshal(probe)
	callCtx, cancel := context.WithTimeout(ctx, domain.CallTimeout)
	defer cancel()
	rendered, err := uc.templates.Render(callCtx, tenantID, ports.RenderRequest{
		TemplateID: templateID,
		Variables:  map[string]json.RawMessage{"confirm_url": probeJSON, "first_name": json.RawMessage(`""`)},
	})
	if err != nil {
		return err
	}
	switch rendered.Kind {
	case kindTransactional:
	case "":
		return domain.ErrTemplateKindUnknown
	default:
		return domain.ErrTemplateNotTransactional
	}
	if !strings.Contains(rendered.HTML, probe) && !strings.Contains(rendered.Text, probe) {
		return domain.ErrTemplateMissingConfirmURL
	}
	return nil
}

// ListDeliveries es el historial del doble opt-in. Nunca incluye el enlace: no se guarda.
func (uc *UseCase) ListDeliveries(ctx context.Context, tenantID uuid.UUID, status domain.DOIStatus, page, perPage int) ([]domain.DOIDelivery, int64, error) {
	return uc.deliveries.List(ctx, tenantID, status, page, perPage)
}

// HandleConsentRequested envia el correo de confirmacion de un contacts.consent.requested.
//
// Devuelve error solo cuando hay que reintentar (el consumidor no hace ack): la base no
// respondio o transactional pidio esperar o no respondio. Los datos invalidos devuelven
// un error de domain.ErrInvalidInput, que no se reintenta. Todo lo demas (enviado,
// omitido, rechazado) queda registrado y se confirma.
//
// El intento se reclama (fila pending) en una transaccion que bloquea al contacto antes
// de llamar a nadie; el envio lleva la clave doi:<event_id>, asi que una reentrega del
// evento o un reintento tras una caida no crea un segundo mensaje.
func (uc *UseCase) HandleConsentRequested(ctx context.Context, req domain.ConsentRequest) (domain.DOIStatus, error) {
	if err := req.Validate(); err != nil {
		return "", err
	}
	now := uc.now()
	var d *domain.DOIDelivery
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		if err := uc.deliveries.LockContact(ctx, req.TenantID, req.ContactID); err != nil {
			return err
		}
		existing, err := uc.deliveries.GetByEvent(ctx, req.TenantID, req.EventID)
		if err != nil {
			return err
		}
		if existing != nil {
			d = existing
			return nil
		}
		settings, err := uc.settings.GetDOISettings(ctx, req.TenantID)
		if err != nil {
			return err
		}
		switch {
		case settings == nil:
			d = domain.NewDOIDelivery(req, nil, domain.DOISkipped, domain.ReasonNotConfigured)
		case !settings.Enabled:
			d = domain.NewDOIDelivery(req, settings, domain.DOISkipped, domain.ReasonDisabled)
		case !settings.Ready():
			d = domain.NewDOIDelivery(req, settings, domain.DOISkipped, domain.ReasonNotConfigured)
		default:
			lastDay, last30, err := uc.deliveries.CountRecent(ctx, req.TenantID, req.ContactID,
				now.Add(-24*time.Hour), now.Add(-30*24*time.Hour), req.EventID)
			if err != nil {
				return err
			}
			if uc.cfg.DOILimits.Allows(lastDay, last30) {
				d = domain.NewDOIDelivery(req, settings, domain.DOIPending, "")
			} else {
				d = domain.NewDOIDelivery(req, settings, domain.DOISkipped, domain.ReasonRateLimited)
			}
		}
		return uc.deliveries.Insert(ctx, d)
	})
	if err != nil {
		return "", err
	}
	if d.Status != domain.DOIPending {
		if d.Reason != "" {
			uc.logger.Info("automations: correo del doble opt-in no enviado",
				zap.String("tenant_id", req.TenantID.String()), zap.String("contact_id", req.ContactID.String()),
				zap.String("status", string(d.Status)), zap.String("reason", d.Reason))
		}
		return d.Status, nil
	}
	return uc.sendDOI(ctx, req, d)
}

func (uc *UseCase) sendDOI(ctx context.Context, req domain.ConsentRequest, d *domain.DOIDelivery) (domain.DOIStatus, error) {
	vars := map[string]any{"confirm_url": req.ConfirmURL}
	if req.FirstName != "" {
		vars["first_name"] = req.FirstName
	}
	callCtx, cancel := context.WithTimeout(ctx, domain.CallTimeout)
	res, err := uc.sender.SendDOI(callCtx, req.TenantID, ports.DOIMessage{
		IdempotencyKey: d.IdempotencyKey(),
		FromEmail:      d.FromEmail,
		FromName:       d.FromName,
		ReplyTo:        d.ReplyTo,
		ToEmail:        req.Email,
		ToName:         req.FirstName,
		TemplateID:     *d.TemplateID,
		Variables:      vars,
		Tags:           map[string]string{"source": "automations", "purpose": "double_opt_in"},
	})
	cancel()

	rctx, rcancel := recordContext(ctx)
	defer rcancel()
	if err == nil {
		if res.Status == ports.MessageStatusSuppressed {
			reason := domain.ReasonSuppressed
			if len(res.Suppressed) > 0 {
				reason += ": " + res.Suppressed[0].Reason
			}
			if _, err := uc.deliveries.MarkFailed(rctx, d.TenantID, d.ID, reason); err != nil {
				return "", err
			}
			return domain.DOIFailed, nil
		}
		if _, err := uc.deliveries.MarkSent(rctx, d.TenantID, d.ID, res.MessageID, uc.now()); err != nil {
			return "", err
		}
		return domain.DOISent, nil
	}

	var blocked *ports.BlockedError
	var rejected *ports.RejectedError
	if errors.As(err, &blocked) || errors.As(err, &rejected) {
		if _, merr := uc.deliveries.MarkFailed(rctx, d.TenantID, d.ID, domain.TruncateReason(err.Error())); merr != nil {
			return "", merr
		}
		uc.logger.Warn("automations: transactional rechazo el correo del doble opt-in",
			zap.String("tenant_id", d.TenantID.String()), zap.String("contact_id", d.ContactID.String()), zap.Error(err))
		return domain.DOIFailed, nil
	}
	if ctx.Err() != nil {
		// Apagado: no es un fallo de transactional ni cuenta como intento.
		return "", ctx.Err()
	}
	attempts, aerr := uc.deliveries.RecordAttempt(rctx, d.TenantID, d.ID, domain.TruncateReason(err.Error()))
	if aerr != nil {
		return "", aerr
	}
	if attempts >= domain.DOIMaxAttempts {
		reason := domain.TruncateReason(fmt.Sprintf("%s tras %d intentos: %v", domain.CodeUnavailable, attempts, err))
		if _, merr := uc.deliveries.MarkFailed(rctx, d.TenantID, d.ID, reason); merr != nil {
			return "", merr
		}
		return domain.DOIFailed, nil
	}
	return "", fmt.Errorf("doble opt-in del evento %s: %w", d.EventID, err)
}
