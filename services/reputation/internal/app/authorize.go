package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Resultados de la autorizacion tal como se cuentan en reputation_authorize_total, ademas
// de los motivos de denegacion del dominio. El motivo que da billing no va a la etiqueta:
// es texto libre y abriria la cardinalidad.
const (
	ResultAllowed    = "allowed"
	ResultPlanDenied = "plan_denied"
)

// AuthorizeInput es la pregunta previa a un envio.
type AuthorizeInput struct {
	TenantID uuid.UUID
	Class    domain.Class
	Count    int64
}

// monthly es lo que se sabe del derecho mensual durante una autorizacion.
type monthly struct {
	key      string
	known    bool
	ent      domain.Entitlement
	consumed int64
}

// Authorize responde si la empresa puede enviar Count mensajes de la clase ahora. El orden
// importa: primero el estado de reputacion, que sale de Postgres y nunca se relaja; despues
// el derecho del plan; por ultimo la tasa, que es lo unico que deja huella (reserva cupo) y
// por eso solo se toca cuando todo lo demas permite.
//
// La reserva de tasa no se devuelve: si el mensaje autorizado luego no sale (falla la
// plantilla, la direccion esta suprimida), su cupo queda consumido hasta que caduque la
// ventana. Devolverlo exigiria que cada llamador confirmara el desenlace de cada envio; el
// coste de no hacerlo es a lo sumo frenar antes de tiempo, nunca dejar pasar de mas.
func (uc *UseCase) Authorize(ctx context.Context, in AuthorizeInput) (domain.Decision, error) {
	if _, err := domain.ParseClass(string(in.Class)); err != nil {
		return domain.Decision{}, err
	}
	if in.Count < 1 || in.Count > domain.MaxAuthorizeCount {
		return domain.Decision{}, domain.ErrInvalidCount
	}
	rec, err := uc.states.Get(ctx, in.TenantID, in.Class)
	if err != nil {
		return domain.Decision{}, err
	}
	override, err := uc.limits.Get(ctx, in.TenantID, in.Class)
	if err != nil {
		return domain.Decision{}, err
	}
	limits := uc.policy.LimitsFor(in.Class, override)
	dec := domain.Decision{
		Class:  in.Class,
		State:  rec.State,
		Hourly: domain.Usage{Limit: limits.Hourly},
		Daily:  domain.Usage{Limit: limits.Daily},
	}

	switch rec.State {
	case domain.StateSuspended:
		return uc.deny(dec, domain.ReasonSuspended, domain.ReasonSuspended), nil
	case domain.StateRestricted:
		return uc.deny(dec, domain.ReasonReputationRestricted, domain.ReasonReputationRestricted), nil
	}

	m := uc.entitlement(ctx, in)
	if m.known {
		view := m.ent.View(m.consumed)
		dec.Monthly = &view
		if ok, reason := m.ent.Admits(in.Count, m.consumed); !ok {
			return uc.deny(dec, reason, ResultPlanDenied), nil
		}
	}

	now := uc.now()
	out, err := uc.rate.Reserve(ctx, in.TenantID, in.Class, now, in.Count, limits)
	if err != nil {
		// Sin Redis no se sabe cuanto lleva la empresa en la hora: se deja pasar (fail-open
		// en la tasa) para no cortar el correo de toda la plataforma por una caida de la
		// cache. El estado de reputacion ya se comprobo contra Postgres y ese no se relaja.
		uc.metrics.Degraded(DependencyRedis)
		uc.logger.Warn("reputation: limite de tasa no disponible; se autoriza sin reservar cupo",
			zap.String("tenant_id", in.TenantID.String()), zap.String("class", string(in.Class)), zap.Error(err))
		return uc.allow(dec, m, in.Count), nil
	}
	dec.Hourly.Used, dec.Daily.Used = &out.HourUsed, &out.DayUsed
	if !out.Allowed {
		dec = uc.deny(dec, domain.ReasonRateLimited, domain.ReasonRateLimited)
		if limits.Fits(in.Count, out.Exceeded) {
			wait := domain.RetryAfter(now, out.Exceeded)
			dec.RetryAfterSeconds = &wait
		}
		return dec, nil
	}
	return uc.allow(dec, m, in.Count), nil
}

// entitlement devuelve el derecho mensual de la cache o de billing. Si billing no responde
// se sigue sin el: el plan no es una barrera de seguridad y no debe cortar el correo.
func (uc *UseCase) entitlement(ctx context.Context, in AuthorizeInput) monthly {
	m := monthly{key: entitlementKey(in.TenantID, in.Class)}
	now := uc.now()
	if ent, consumed, ok := uc.ent.get(m.key, now); ok {
		m.ent, m.consumed, m.known = ent, consumed, true
		return m
	}
	ent, err := uc.billing.Check(ctx, in.TenantID, in.Class, in.Count)
	if err != nil {
		uc.metrics.Degraded(DependencyBilling)
		uc.logger.Warn("reputation: billing no respondio; se autoriza sin derecho mensual",
			zap.String("tenant_id", in.TenantID.String()), zap.String("class", string(in.Class)), zap.Error(err))
		return m
	}
	uc.ent.put(m.key, ent, now)
	m.ent, m.known = ent, true
	return m
}

func (uc *UseCase) deny(dec domain.Decision, reason, result string) domain.Decision {
	dec.Allowed, dec.Reason = false, reason
	uc.metrics.Authorize(dec.Class, result)
	return dec
}

func (uc *UseCase) allow(dec domain.Decision, m monthly, count int64) domain.Decision {
	if m.known {
		uc.ent.consume(m.key, count)
		view := m.ent.View(m.consumed + count)
		dec.Monthly = &view
	}
	dec.Allowed, dec.Reason = true, ""
	uc.metrics.Authorize(dec.Class, ResultAllowed)
	return dec
}
