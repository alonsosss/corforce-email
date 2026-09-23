package app

import (
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/analytics/internal/domain"
	"github.com/alonsosss/corforce-email/services/analytics/internal/ports"
)

// ProcessedEventsRetention es cuanto se recuerda un evento ya contado. Supera la
// retencion de los streams (7 dias): un evento no puede reentregarse despues de olvidado.
const ProcessedEventsRetention = 30 * 24 * time.Hour

type Deps struct {
	Tx        ports.Transactor
	Ledger    ports.EventLedger
	Facts     ports.FactRepository
	Stats     ports.StatsRepository
	Campaigns ports.CampaignRepository
	Links     ports.LinkRepository
	Reports   ports.ReportRepository
	// MessageRetention es cuanto se conserva la fila de un mensaje sin actividad
	// (ANALYTICS_MESSAGE_RETENTION_DAYS). Los agregados no se podan.
	MessageRetention time.Duration
	// Now permite fijar el reloj en las pruebas; nil = time.Now en UTC.
	Now func() time.Time
}

type UseCase struct {
	tx        ports.Transactor
	ledger    ports.EventLedger
	facts     ports.FactRepository
	stats     ports.StatsRepository
	campaigns ports.CampaignRepository
	links     ports.LinkRepository
	reports   ports.ReportRepository
	retention time.Duration
	now       func() time.Time
}

func New(d Deps) *UseCase {
	now := d.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &UseCase{
		tx: d.Tx, ledger: d.Ledger, facts: d.Facts, stats: d.Stats, campaigns: d.Campaigns,
		links: d.Links, reports: d.Reports, retention: d.MessageRetention, now: now,
	}
}

// IsPermanent distingue el evento que no se podra contar nunca (se descarta con ack) del
// fallo transitorio de la base (se reintenta).
func IsPermanent(err error) bool { return errors.Is(err, domain.ErrInvalidEvent) }
