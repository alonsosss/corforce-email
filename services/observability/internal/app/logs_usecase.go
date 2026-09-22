package app

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/observability/internal/domain"
	"github.com/alonsosss/corforce-email/services/observability/internal/ports"
	"go.uber.org/zap"
)

// Desenlaces de una consulta, para la metrica observability_log_queries_total.
const (
	OutcomeOK            = "ok"
	OutcomeInvalid       = "invalid"
	OutcomeNotConfigured = "not_configured"
	OutcomeUnavailable   = "unavailable"
	OutcomeError         = "error"
	// outcomeServiceUnknown es la etiqueta de servicio de una consulta rechazada antes de saber cual pedia.
	outcomeServiceUnknown = "unknown"
)

// LogsUseCase es el visor de registros: una consulta acotada a un servicio de la lista blanca, con un
// unico filtro de texto literal y una ventana de tiempo maxima, contra Loki. Los registros de la
// plataforma mezclan a todas las empresas (una linea de Postfix lleva remitente y destinatario), asi que
// solo los lee el superadmin: el permiso es de plataforma y aqui se exige de nuevo. Cada consulta queda
// en el registro con quien la hizo, el servicio y la ventana; nunca el texto buscado, que puede ser una
// direccion de correo de un cliente.
type LogsUseCase struct {
	store   ports.LogStore
	metrics ports.LogMetrics
	logger  *zap.Logger
	now     func() time.Time
}

type LogsDeps struct {
	// Store nil deja el visor desactivado (503 NOT_CONFIGURED).
	Store   ports.LogStore
	Metrics ports.LogMetrics
	Logger  *zap.Logger
	// Now fija el reloj de la validacion; nil usa time.Now.
	Now func() time.Time
}

func NewLogsUseCase(d LogsDeps) *LogsUseCase {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &LogsUseCase{store: d.Store, metrics: d.Metrics, logger: d.Logger, now: now}
}

// Services devuelve la lista blanca de servicios consultables.
func (uc *LogsUseCase) Services(platform bool) ([]string, error) {
	if !platform {
		return nil, domain.ErrPlatformOnly
	}
	return domain.LogServices(), nil
}

// Query valida la peticion, construye la consulta y la ejecuta. actor es quien la pide y queda en el
// registro.
func (uc *LogsUseCase) Query(ctx context.Context, platform bool, actor string, in domain.LogQueryInput) (domain.LogPage, error) {
	if !platform {
		return domain.LogPage{}, domain.ErrPlatformOnly
	}
	q, err := domain.NewLogQuery(in, uc.now().UTC())
	if err != nil {
		uc.observe(outcomeServiceUnknown, OutcomeInvalid)
		return domain.LogPage{}, err
	}
	if uc.store == nil {
		uc.observe(q.Service, OutcomeNotConfigured)
		return domain.LogPage{}, domain.ErrNotConfigured
	}
	entries, err := uc.store.QueryRange(ctx, q.LogQL(), q.Since, q.Until, q.Limit, q.Direction)
	if err != nil {
		outcome := OutcomeError
		if errors.Is(err, domain.ErrStoreUnavailable) {
			outcome = OutcomeUnavailable
		}
		uc.observe(q.Service, outcome)
		uc.logger.Warn("consulta de registros fallida", zap.String("service", q.Service), zap.String("actor", actor), zap.Error(err))
		return domain.LogPage{}, err
	}
	entries = domain.SortEntries(entries, q.Direction, q.Limit)
	if entries == nil {
		entries = []domain.LogEntry{}
	}
	uc.observe(q.Service, OutcomeOK)
	uc.logger.Info("consulta de registros",
		zap.String("service", q.Service), zap.Time("since", q.Since), zap.Time("until", q.Until),
		zap.Int("limit", q.Limit), zap.String("direction", string(q.Direction)),
		zap.Bool("filtered", q.Text != ""), zap.Int("entries", len(entries)), zap.String("actor", actor))
	return domain.LogPage{
		Service: q.Service, Since: q.Since, Until: q.Until, Direction: q.Direction, Limit: q.Limit,
		Truncated: len(entries) >= q.Limit, Entries: entries,
	}, nil
}

func (uc *LogsUseCase) observe(service, outcome string) {
	if uc.metrics != nil {
		uc.metrics.LogQueryObserved(service, outcome)
	}
}
