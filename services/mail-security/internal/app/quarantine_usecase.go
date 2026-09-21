package app

import (
	"context"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// QuarantineUseCase es la cuarentena vista desde el API de administracion y desde los
// enlaces sin sesion del aviso.
type QuarantineUseCase struct {
	tx      ports.Transactor
	repo    ports.QuarantineRepository
	reinj   ports.Reinjector
	learner ports.SpamLearner
	events  ports.EventPublisher
	notices ports.QuarantineNoticeRepository
	links   *domain.QuarantineLinkSigner
	metrics ports.QuarantineMetrics
	logger  *zap.Logger
	now     func() time.Time
}

type QuarantineDeps struct {
	Tx         ports.Transactor
	Repo       ports.QuarantineRepository
	Reinjector ports.Reinjector
	Learner    ports.SpamLearner
	Events     ports.EventPublisher
	// Notices y Links sirven los enlaces del aviso; sin Links todo enlace es invalido.
	Notices ports.QuarantineNoticeRepository
	Links   *domain.QuarantineLinkSigner
	// Metrics es opcional: sin ellas no se cuenta nada.
	Metrics ports.QuarantineMetrics
	Logger  *zap.Logger
}

func NewQuarantineUseCase(d QuarantineDeps) *QuarantineUseCase {
	return &QuarantineUseCase{tx: d.Tx, repo: d.Repo, reinj: d.Reinjector, learner: d.Learner, events: d.Events,
		notices: d.Notices, links: d.Links, metrics: quarantineMetricsOrNoop(d.Metrics), logger: d.Logger, now: time.Now}
}

func (uc *QuarantineUseCase) List(ctx context.Context, tenantID uuid.UUID, f domain.QuarantineFilter) (items []domain.QuarantineItem, total int64, err error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PerPage < 1 {
		f.PerPage = domain.DefaultQuarantinePerPage
	}
	if f.PerPage > domain.MaxQuarantinePerPage {
		f.PerPage = domain.MaxQuarantinePerPage
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
	err := uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		return uc.repo.Delete(ctx, tenantID, id)
	})
	if err == nil {
		uc.metrics.QuarantineDiscarded()
	}
	return err
}

// Release reinyecta el mensaje por el puerto interno de Postfix (sin milter) al buzon
// final, borra la fila y encola el evento en UNA transaccion que bloquea la fila: dos
// liberaciones simultaneas no entregan el mensaje dos veces (la segunda ya no lo
// encuentra), y si la reinyeccion falla no se borra nada y se puede reintentar. El coste es
// mantener la transaccion abierta durante la entrega SMTP local, que acota el timeout del
// reinyector.
func (uc *QuarantineUseCase) Release(ctx context.Context, tenantID, id uuid.UUID, userID string) error {
	return uc.release(ctx, tenantID, id, userID, nil)
}

// release es Release con una comprobacion opcional que corre con la fila ya bloqueada y
// antes de reinyectar (la usa el enlace sin sesion para registrar su uso): si falla, no se
// entrega ni se borra nada.
func (uc *QuarantineUseCase) release(ctx context.Context, tenantID, id uuid.UUID, userID string, guard func(ctx context.Context, item *domain.QuarantineItem) error) error {
	reinjected := false
	err := uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		item, err := uc.repo.LockForRelease(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if guard != nil {
			if err := guard(ctx, item); err != nil {
				return err
			}
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
	if err == nil {
		uc.metrics.QuarantineReleased()
	}
	return err
}

// ReleaseAndLearnHam libera el mensaje y, ya liberado, entrena el clasificador con el como legitimo. El
// entrenamiento va despues y no deshace la liberacion: si el controller de Rspamd no responde, el dueno ya
// tiene su correo y el fallo solo se registra. Es un acto explicito, distinto de liberar: quien libera por
// prisa un spam no debe envenenar el clasificador.
func (uc *QuarantineUseCase) ReleaseAndLearnHam(ctx context.Context, tenantID, id uuid.UUID, userID string) error {
	if uc.learner == nil {
		return domain.ErrNotConfigured
	}
	var msg []byte
	err := uc.release(ctx, tenantID, id, userID, func(_ context.Context, item *domain.QuarantineItem) error {
		msg = item.Msg
		return nil
	})
	if err != nil {
		return err
	}
	if lerr := uc.learner.LearnHam(ctx, msg); lerr != nil {
		uc.logger.Warn("mensaje liberado pero no aprendido como legitimo", zap.String("id", id.String()), zap.Error(lerr))
		return nil
	}
	uc.metrics.QuarantineLearnedHam()
	return nil
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
	if err := uc.learner.LearnSpam(ctx, msg); err != nil {
		return err
	}
	uc.metrics.QuarantineLearnedSpam()
	return nil
}

type noopQuarantineMetrics struct{}

func (noopQuarantineMetrics) QuarantineStored()      {}
func (noopQuarantineMetrics) QuarantineReleased()    {}
func (noopQuarantineMetrics) QuarantineDiscarded()   {}
func (noopQuarantineMetrics) QuarantineLearnedSpam() {}
func (noopQuarantineMetrics) QuarantineLearnedHam()  {}

func quarantineMetricsOrNoop(m ports.QuarantineMetrics) ports.QuarantineMetrics {
	if m == nil {
		return noopQuarantineMetrics{}
	}
	return m
}
