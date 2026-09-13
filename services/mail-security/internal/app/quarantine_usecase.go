package app

import (
	"context"
	"fmt"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Tope de pagina del listado de cuarentena.
const (
	defaultQuarantinePerPage = 50
	maxQuarantinePerPage     = 200
)

// QuarantineUseCase es la cuarentena vista desde el API de administracion.
type QuarantineUseCase struct {
	tx      ports.Transactor
	repo    ports.QuarantineRepository
	reinj   ports.Reinjector
	learner ports.SpamLearner
	events  ports.EventPublisher
	logger  *zap.Logger
}

type QuarantineDeps struct {
	Tx         ports.Transactor
	Repo       ports.QuarantineRepository
	Reinjector ports.Reinjector
	Learner    ports.SpamLearner
	Events     ports.EventPublisher
	Logger     *zap.Logger
}

func NewQuarantineUseCase(d QuarantineDeps) *QuarantineUseCase {
	return &QuarantineUseCase{tx: d.Tx, repo: d.Repo, reinj: d.Reinjector, learner: d.Learner, events: d.Events, logger: d.Logger}
}

func (uc *QuarantineUseCase) List(ctx context.Context, tenantID uuid.UUID, f domain.QuarantineFilter) (items []domain.QuarantineItem, total int64, err error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PerPage < 1 {
		f.PerPage = defaultQuarantinePerPage
	}
	if f.PerPage > maxQuarantinePerPage {
		f.PerPage = maxQuarantinePerPage
	}
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		items, total, err = uc.repo.List(ctx, tenantID, f)
		return err
	})
	return items, total, err
}

func (uc *QuarantineUseCase) Get(ctx context.Context, tenantID, id uuid.UUID) (out *domain.QuarantineItem, err error) {
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		out, err = uc.repo.Get(ctx, tenantID, id)
		return err
	})
	return out, err
}

func (uc *QuarantineUseCase) Message(ctx context.Context, tenantID, id uuid.UUID) (msg []byte, err error) {
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		msg, err = uc.repo.GetMessage(ctx, tenantID, id)
		return err
	})
	return msg, err
}

func (uc *QuarantineUseCase) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		return uc.repo.Delete(ctx, tenantID, id)
	})
}

// Release reinyecta el mensaje por el puerto interno de Postfix (sin milter) al buzon
// final, borra la fila y encola el evento en UNA transaccion que bloquea la fila: dos
// liberaciones simultaneas no entregan el mensaje dos veces (la segunda ya no lo
// encuentra), y si la reinyeccion falla no se borra nada y se puede reintentar. El coste es
// mantener la transaccion abierta durante la entrega SMTP local, que acota el timeout del
// reinyector.
func (uc *QuarantineUseCase) Release(ctx context.Context, tenantID, id uuid.UUID, userID string) error {
	reinjected := false
	err := uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		item, err := uc.repo.LockForRelease(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if err := uc.reinj.Reinject(ctx, item.Sender, item.Rcpt, item.Msg); err != nil {
			return fmt.Errorf("reinyectar %s: %w", id, err)
		}
		reinjected = true
		if err := uc.repo.Delete(ctx, tenantID, id); err != nil {
			return err
		}
		return uc.events.QuarantineReleased(ctx, item, userID)
	})
	if err != nil && reinjected {
		// El mensaje ya se entrego pero la fila sigue: se deja constancia para que nadie lo
		// libere de nuevo sin saberlo.
		uc.logger.Error("mensaje reinyectado sin confirmar la liberacion; la fila sigue en cuarentena",
			zap.String("id", id.String()), zap.Error(err))
	}
	return err
}

// LearnSpam entrena el clasificador con el mensaje. La fila se conserva.
func (uc *QuarantineUseCase) LearnSpam(ctx context.Context, tenantID, id uuid.UUID) error {
	if uc.learner == nil {
		return domain.ErrNotConfigured
	}
	msg, err := uc.Message(ctx, tenantID, id)
	if err != nil {
		return err
	}
	return uc.learner.LearnSpam(ctx, msg)
}
