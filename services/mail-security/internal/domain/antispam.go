package domain

import (
	"sort"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

const (
	// MaxRspamdHistoryRows es el tope de filas del historial de Rspamd en una consulta.
	MaxRspamdHistoryRows = 200
	// DefaultRspamdHistoryRows es lo que devuelve una consulta sin limite.
	DefaultRspamdHistoryRows = 50
	// MaxRspamdSubjectRunes recorta el asunto de cada fila: acota la respuesta y es lo que cabe en una tabla.
	MaxRspamdSubjectRunes = 200
)

// RspamdStats son los contadores de GET /stat del controller de Rspamd: cuanto analizo, con que veredicto,
// el tamano del clasificador bayesiano y del fuzzy y los tiempos de analisis. Solo lectura: nada de
// configuracion ni de pesos.
type RspamdStats struct {
	Version            string           `json:"version"`
	UptimeSeconds      int64            `json:"uptime_seconds"`
	Scanned            int64            `json:"scanned"`
	Learned            int64            `json:"learned"`
	SpamCount          int64            `json:"spam_count"`
	HamCount           int64            `json:"ham_count"`
	Actions            map[string]int64 `json:"actions"`
	Connections        int64            `json:"connections"`
	ControlConnections int64            `json:"control_connections"`
	TotalLearns        int64            `json:"total_learns"`
	Statfiles          []RspamdStatfile `json:"statfiles"`
	FuzzyHashes        map[string]int64 `json:"fuzzy_hashes"`
	ScanTime           RspamdScanTime   `json:"scan_time"`
}

// RspamdStatfile es un fichero del clasificador bayesiano (BAYES_SPAM, BAYES_HAM).
type RspamdStatfile struct {
	Symbol    string `json:"symbol"`
	Type      string `json:"type"`
	Revision  int64  `json:"revision"`
	Used      int64  `json:"used"`
	Total     int64  `json:"total"`
	Size      int64  `json:"size"`
	Languages int64  `json:"languages"`
	Users     int64  `json:"users"`
}

// RspamdScanTime resume los ultimos tiempos de analisis que el controller conserva, en milisegundos.
type RspamdScanTime struct {
	Samples   int             `json:"samples"`
	AverageMs decimal.Decimal `json:"average_ms"`
	MaxMs     decimal.Decimal `json:"max_ms"`
}

// NewRspamdScanTime resume una lista de tiempos en segundos.
func NewRspamdScanTime(seconds []decimal.Decimal) RspamdScanTime {
	out := RspamdScanTime{Samples: len(seconds), AverageMs: decimal.Zero, MaxMs: decimal.Zero}
	if len(seconds) == 0 {
		return out
	}
	mil := decimal.NewFromInt(1000)
	sum := decimal.Zero
	for _, s := range seconds {
		ms := s.Mul(mil)
		sum = sum.Add(ms)
		if ms.GreaterThan(out.MaxMs) {
			out.MaxMs = ms
		}
	}
	out.AverageMs = sum.Div(decimal.NewFromInt(int64(len(seconds)))).Round(2)
	out.MaxMs = out.MaxMs.Round(2)
	return out
}

// RspamdSymbol es un simbolo que Rspamd aplico a un mensaje, con su peso. Sus opciones (URLs, direcciones
// IP, fragmentos del mensaje) se descartan a proposito: acotan la respuesta y no hacen falta para leer
// el veredicto.
type RspamdSymbol struct {
	Name  string          `json:"name"`
	Score decimal.Decimal `json:"score"`
}

// RspamdHistoryRow es una fila del historial reciente de Rspamd: el sobre y el veredicto de un mensaje
// analizado, nunca su contenido. Remitente, destinatarios y asunto son de toda la celda, como la cola de
// Postfix y la cuarentena que el superadmin ya ve.
type RspamdHistoryRow struct {
	ID            string          `json:"id"`
	Time          time.Time       `json:"time"`
	IP            string          `json:"ip"`
	User          string          `json:"user,omitempty"`
	Sender        string          `json:"sender"`
	Recipients    []string        `json:"recipients"`
	Subject       string          `json:"subject"`
	Score         decimal.Decimal `json:"score"`
	RequiredScore decimal.Decimal `json:"required_score"`
	Action        string          `json:"action"`
	Symbols       []RspamdSymbol  `json:"symbols"`
	Size          int64           `json:"size"`
	ScanTimeMs    decimal.Decimal `json:"scan_time_ms"`
	Skipped       bool            `json:"skipped"`
}

// RspamdHistory es una consulta del historial: Total cuenta las filas que el controller devolvio aunque
// Rows traiga menos.
type RspamdHistory struct {
	Total     int                `json:"total"`
	Truncated bool               `json:"truncated"`
	Rows      []RspamdHistoryRow `json:"rows"`
}

// NewestRspamdHistory ordena las filas de la mas reciente a la mas antigua y se queda con limit.
func NewestRspamdHistory(rows []RspamdHistoryRow, limit int) RspamdHistory {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Time.After(rows[j].Time) })
	out := RspamdHistory{Total: len(rows), Rows: rows}
	if limit > 0 && len(rows) > limit {
		out.Rows = rows[:limit]
		out.Truncated = true
	}
	if out.Rows == nil {
		out.Rows = []RspamdHistoryRow{}
	}
	return out
}

// TruncateRspamdSubject recorta un asunto a MaxRspamdSubjectRunes.
func TruncateRspamdSubject(subject string) string {
	if utf8.RuneCountInString(subject) <= MaxRspamdSubjectRunes {
		return subject
	}
	runes := []rune(subject)
	return string(runes[:MaxRspamdSubjectRunes])
}
