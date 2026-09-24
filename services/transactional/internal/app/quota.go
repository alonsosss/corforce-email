package app

import (
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"go.uber.org/zap"
)

// marketingQuotaDeferral es cuanto se aplaza un mensaje de marketing cuando la cuenta de SES entra
// en la reserva del transaccional: del orden del intervalo del vigilante, que es cuando puede
// cambiar la decision.
const marketingQuotaDeferral = 10 * time.Minute

// quotaGate guarda la ultima lectura de la cuenta de SES y lo enviado desde entonces. Sin lectura, o
// con una lectura mas vieja que maxAge (el vigilante apagado o fallando), no frena: SES aplica su
// propia cuota y un vigilante caido no debe detener las campanas.
type quotaGate struct {
	mu        sync.Mutex
	status    *domain.SESAccountStatus
	readAt    time.Time
	maxAge    time.Duration
	sentSince int64
	closed    bool
}

func (g *quotaGate) observe(status domain.SESAccountStatus, now time.Time, maxAge time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.status, g.readAt, g.maxAge, g.sentSince = &status, now, maxAge, 0
}

func (g *quotaGate) noteSent() {
	g.mu.Lock()
	g.sentSince++
	g.mu.Unlock()
}

// marketingOpen decide y devuelve si la decision cambio respecto de la anterior, para avisar solo en
// el cambio y no en cada mensaje.
func (g *quotaGate) marketingOpen(now time.Time, reserve float64) (open, changed bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	open = g.status == nil || now.Sub(g.readAt) > g.maxAge || g.status.MarketingQuotaOpen(g.sentSince, reserve)
	changed = open == g.closed
	g.closed = !open
	return open, changed
}

// marketingQuotaOpen dice si un mensaje de marketing puede salir ahora sin entrar en la reserva de la
// cuota diaria del transaccional.
func (uc *UseCase) marketingQuotaOpen() bool {
	open, changed := uc.quota.marketingOpen(uc.now(), uc.cfg.MarketingQuotaReserve)
	if changed && uc.logger != nil {
		if open {
			uc.logger.Info("transactional: la cuenta de SES salio de la reserva del transaccional; el marketing vuelve a salir")
		} else {
			uc.logger.Warn("transactional: la cuenta de SES entro en la reserva del transaccional; el marketing se aplaza",
				zap.Float64("reserve", uc.cfg.MarketingQuotaReserve), zap.Duration("deferral", marketingQuotaDeferral))
		}
	}
	return open
}
