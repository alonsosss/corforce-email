package app

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// HandleTrigger hace entrar al contacto del evento en cada flujo activo que lo acepta.
//
// La pertenencia a la lista se pregunta a contacts ANTES de abrir la transaccion (una
// llamada de red no retiene una conexion). La transaccion registra el evento como
// procesado y crea las ejecuciones a la vez: si cae antes del commit no queda nada y la
// reentrega lo repite entero; si ya se proceso, no se crea nada. Ademas cada ejecucion es
// unica por (flujo, contacto, evento), asi que ni una carrera entre dos entregas duplica.
//
// Devuelve cuantas ejecuciones se crearon. Un error distinto de domain.ErrInvalidInput
// significa reintentar.
func (uc *UseCase) HandleTrigger(ctx context.Context, ev domain.TriggerEvent) (int, error) {
	if ev.EventID == "" || ev.TenantID == uuid.Nil || ev.ContactID == uuid.Nil {
		return 0, domain.NewValidationError("evento de disparo sin id, empresa o contacto")
	}
	done, err := uc.processed.IsProcessed(ctx, ev.TenantID, ev.EventID)
	if err != nil || done {
		return 0, err
	}
	active, err := uc.workflows.ListActiveByTrigger(ctx, ev.TenantID, ev.Type)
	if err != nil {
		return 0, err
	}
	var candidates []domain.Workflow
	for i := range active {
		if active[i].Accepts(ev) {
			candidates = append(candidates, active[i])
		}
	}
	inList, err := uc.listMembership(ctx, ev, candidates)
	if err != nil {
		return 0, err
	}

	now := uc.now()
	entered := 0
	err = uc.tx.Transact(ctx, func(ctx context.Context) error {
		entered = 0
		fresh, err := uc.processed.MarkProcessed(ctx, ev.TenantID, ev.EventID, now)
		if err != nil || !fresh {
			return err
		}
		for i := range candidates {
			w := &candidates[i]
			if w.ListID != nil && !inList[*w.ListID] {
				continue
			}
			ok, err := uc.runs.Enroll(ctx, domain.NewRun(w, ev, now), w.ReEntry)
			if err != nil {
				return err
			}
			if ok {
				entered++
			}
		}
		return nil
	})
	return entered, err
}

// listMembership pregunta a contacts, una vez por lista, si el contacto es miembro. Una
// lista que ya no existe deja fuera a los flujos que la usan (y se avisa); cualquier otro
// fallo se reintenta.
func (uc *UseCase) listMembership(ctx context.Context, ev domain.TriggerEvent, candidates []domain.Workflow) (map[uuid.UUID]bool, error) {
	out := make(map[uuid.UUID]bool)
	for _, w := range candidates {
		if w.ListID == nil {
			continue
		}
		if _, asked := out[*w.ListID]; asked {
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, domain.CallTimeout)
		members, err := uc.contacts.ListMembers(callCtx, ev.TenantID, *w.ListID, []uuid.UUID{ev.ContactID})
		cancel()
		var rejected *ports.RejectedError
		switch {
		case errors.As(err, &rejected) && rejected.NotFound():
			uc.logger.Warn("automations: la lista del filtro de un flujo ya no existe; nadie entra por ella",
				zap.String("tenant_id", ev.TenantID.String()), zap.String("workflow_id", w.ID.String()),
				zap.String("list_id", w.ListID.String()))
			out[*w.ListID] = false
			continue
		case err != nil:
			return nil, err
		}
		member := false
		for _, id := range members {
			member = member || id == ev.ContactID
		}
		out[*w.ListID] = member
	}
	return out, nil
}
