package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/observability/internal/domain"
)

// LogStore es el almacen de registros visto desde este servicio (Loki). Recibe la consulta LogQL ya
// construida por el dominio y devuelve las lineas de la ventana. Los errores envuelven
// domain.ErrStoreUnavailable o domain.ErrStoreRejected.
type LogStore interface {
	QueryRange(ctx context.Context, query string, since, until time.Time, limit int, direction domain.LogDirection) ([]domain.LogEntry, error)
}

// LogMetrics anota cada consulta del visor por servicio y desenlace.
type LogMetrics interface {
	LogQueryObserved(service, outcome string)
}
