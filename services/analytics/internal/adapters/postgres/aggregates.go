package postgres

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/alonsosss/corforce-email/services/analytics/internal/domain"
)

// counterColumns son las columnas de contadores en el orden de domain.Counter.
var counterColumns = [domain.CounterCount]string{
	"sent", "delivered", "bounced_hard", "bounced_soft", "complained",
	"opened_unique", "clicked_unique", "unsubscribed", "failed",
}

// counterDest son los destinos de Scan de unos contadores, en el orden de counterColumns.
func counterDest(c *domain.Counters) []any {
	return []any{
		&c.Sent, &c.Delivered, &c.BouncedHard, &c.BouncedSoft, &c.Complained,
		&c.OpenedUnique, &c.ClickedUnique, &c.Unsubscribed, &c.Failed,
	}
}

func counterArgs(c domain.Counters) []any {
	values := c.Values()
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

// aggregate son las dos sentencias de escritura de un agregado diario. Se generan una vez
// a partir de identificadores constantes (nunca de datos) para que columnas, marcadores y
// argumentos no puedan desalinearse entre las tres tablas.
type aggregate struct {
	name string
	// upsert suma contadores no negativos, creando la fila si no existe.
	upsert string
	// increment aplica contadores con algun descuento: la fila debe existir, porque un
	// descuento corrige una suma anterior de esa misma fila.
	increment string
}

func newAggregate(table string, keys ...string) aggregate {
	short := table[strings.IndexByte(table, '.')+1:]
	cols := append(append([]string{}, keys...), counterColumns[:]...)
	marks := make([]string, len(cols))
	for i := range cols {
		marks[i] = "$" + strconv.Itoa(i+1)
	}
	sums := make([]string, len(counterColumns))
	incs := make([]string, len(counterColumns))
	for i, c := range counterColumns {
		sums[i] = fmt.Sprintf("%s = %s.%s + EXCLUDED.%s", c, short, c, c)
		incs[i] = fmt.Sprintf("%s = %s + $%d", c, c, len(keys)+i+1)
	}
	where := make([]string, len(keys))
	for i, k := range keys {
		where[i] = fmt.Sprintf("%s = $%d", k, i+1)
	}
	return aggregate{
		name: table,
		upsert: fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) DO UPDATE SET %s",
			table, strings.Join(cols, ", "), strings.Join(marks, ", "), strings.Join(keys, ", "), strings.Join(sums, ", ")),
		increment: fmt.Sprintf("UPDATE %s SET %s WHERE %s",
			table, strings.Join(incs, ", "), strings.Join(where, " AND ")),
	}
}

var (
	classAggregate    = newAggregate("analytics.daily_class_stats", "tenant_id", "day", "class")
	campaignAggregate = newAggregate("analytics.daily_campaign_stats", "tenant_id", "day", "campaign_id")
	domainAggregate   = newAggregate("analytics.daily_domain_stats", "tenant_id", "day", "class", "recipient_domain")
)

// sumColumns es la lista de sumas de contadores de una consulta; alias es el prefijo de
// tabla ("" o "s.").
func sumColumns(alias string) string {
	out := make([]string, len(counterColumns))
	for i, c := range counterColumns {
		out[i] = fmt.Sprintf("COALESCE(sum(%s%s), 0)::bigint", alias, c)
	}
	return strings.Join(out, ", ")
}

// coalesceColumns es la lista de contadores de una fila opcional (LEFT JOIN).
func coalesceColumns(alias string) string {
	out := make([]string, len(counterColumns))
	for i, c := range counterColumns {
		out[i] = fmt.Sprintf("COALESCE(%s%s, 0)", alias, c)
	}
	return strings.Join(out, ", ")
}
