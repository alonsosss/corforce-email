package domain

import (
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// logServices es la lista blanca del visor: los nombres de servicio de compose que promtail pone en la
// etiqueta `servicio` (ops/observability/promtail/promtail.yml), es decir los motores de
// deploy/mail/docker-compose.mail.yml y los servicios Go de docker-compose.yml. Es fija a proposito: el
// nombre entra en el selector de flujo de la consulta LogQL y nunca sale del cliente. Una prueba la
// contrasta con los dos ficheros de compose para que un servicio nuevo no quede fuera por olvido.
var logServices = []string{
	// Motores de correo (deploy/mail).
	"postfix-mail", "dovecot-mail", "rspamd-mail", "clamd-mail", "unbound-mail", "olefy-mail",
	"postfix-tlspol-mail", "redis-mail", "netfilter-mail", "acme-mail", "watchdog-mail", "dockerapi-mail",
	"mail-migration-runner",
	// Plano de control.
	"gateway", "identity", "access-control", "organization", "audit", "scheduler", "observability",
	// Correo corporativo.
	"mail-directory", "mail-auth", "mail-security", "webmail", "domain-service", "mail-migration", "mail-dav",
	"mail-files",
	// Correo transaccional y marketing.
	"transactional", "suppression", "templates", "contacts", "billing", "reputation", "campaigns",
	"automations", "analytics", "smtp-relay",
}

// LogServices devuelve una copia ordenada de la lista blanca.
func LogServices() []string {
	out := append([]string(nil), logServices...)
	sort.Strings(out)
	return out
}

// IsLogService dice si name esta en la lista blanca, con comparacion exacta.
func IsLogService(name string) bool {
	for _, s := range logServices {
		if s == name {
			return true
		}
	}
	return false
}

// LogDirection es el orden de las lineas: las mas recientes primero (backward) o las mas antiguas (forward).
type LogDirection string

const (
	DirectionBackward LogDirection = "backward"
	DirectionForward  LogDirection = "forward"
)

const (
	// MaxLogLimit es el tope de lineas de una consulta; DefaultLogLimit lo que devuelve una sin limite.
	MaxLogLimit     = 500
	DefaultLogLimit = 100
	// MaxLogWindow acota la ventana de tiempo de una consulta; DefaultLogWindow es la ventana sin since.
	MaxLogWindow     = 24 * time.Hour
	DefaultLogWindow = time.Hour
	// MaxLogTextRunes es el largo maximo del texto buscado, en runas.
	MaxLogTextRunes = 200
	// futureSlack tolera relojes algo adelantados en until sin admitir ventanas en el futuro.
	futureSlack = 5 * time.Minute
)

// LogQuery es una consulta ya validada: lo unico que llega a construir LogQL.
type LogQuery struct {
	Service   string
	Text      string
	Since     time.Time
	Until     time.Time
	Limit     int
	Direction LogDirection
}

// LogQueryInput es lo que pide el cliente, antes de validar. since y until nulos toman los defectos.
type LogQueryInput struct {
	Service   string
	Text      string
	Since     *time.Time
	Until     *time.Time
	Limit     int
	Direction string
}

// NewLogQuery valida y normaliza la peticion. now fija los defectos de la ventana (hasta ahora, desde hace
// una hora) y el tope hacia el futuro.
func NewLogQuery(in LogQueryInput, now time.Time) (LogQuery, error) {
	q := LogQuery{Service: strings.TrimSpace(in.Service)}
	if !IsLogService(q.Service) {
		return LogQuery{}, newValidation("service debe ser uno de los servicios que sirve /logs/services")
	}
	text, err := validateText(in.Text)
	if err != nil {
		return LogQuery{}, err
	}
	q.Text = text

	q.Until = now
	if in.Until != nil {
		q.Until = in.Until.UTC()
	}
	if q.Until.After(now.Add(futureSlack)) {
		return LogQuery{}, newValidation("until no puede estar en el futuro")
	}
	q.Since = q.Until.Add(-DefaultLogWindow)
	if in.Since != nil {
		q.Since = in.Since.UTC()
	}
	if !q.Since.Before(q.Until) {
		return LogQuery{}, newValidation("since debe ser anterior a until")
	}
	if q.Until.Sub(q.Since) > MaxLogWindow {
		return LogQuery{}, newValidation("la ventana no puede superar las 24 horas")
	}

	switch in.Limit {
	case 0:
		q.Limit = DefaultLogLimit
	default:
		if in.Limit < 0 {
			return LogQuery{}, newValidation("limit debe ser un entero positivo")
		}
		q.Limit = in.Limit
		if q.Limit > MaxLogLimit {
			q.Limit = MaxLogLimit
		}
	}

	switch LogDirection(in.Direction) {
	case "", DirectionBackward:
		q.Direction = DirectionBackward
	case DirectionForward:
		q.Direction = DirectionForward
	default:
		return LogQuery{}, newValidation("direction debe ser backward o forward")
	}
	return q, nil
}

// validateText admite solo texto imprimible en UTF-8 valido, sin saltos de linea ni otros caracteres de
// control, de como mucho MaxLogTextRunes. Vacio significa sin filtro.
func validateText(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", nil
	}
	if !utf8.ValidString(text) {
		return "", newValidation("q debe ser UTF-8 válido")
	}
	if utf8.RuneCountInString(text) > MaxLogTextRunes {
		return "", newValidation("q no puede superar los 200 caracteres")
	}
	for _, r := range text {
		if unicode.IsControl(r) {
			return "", newValidation("q no admite saltos de línea ni caracteres de control")
		}
	}
	return text, nil
}

// LogQL construye la consulta: un selector de flujo por el servicio de la lista blanca y, si hay texto, UN
// filtro de linea literal. El texto se escapa como literal de cadena de LogQL, que sigue la gramatica de las
// cadenas de Go (Loki lo decodifica con strutil.Unquote): comillas y barras quedan escapadas y ningun
// caracter del usuario puede cerrar la cadena ni anadir otra etapa a la consulta.
func (q LogQuery) LogQL() string {
	var b strings.Builder
	b.WriteString(`{servicio=`)
	b.WriteString(strconv.Quote(q.Service))
	b.WriteString(`}`)
	if q.Text != "" {
		b.WriteString(` |= `)
		b.WriteString(strconv.Quote(q.Text))
	}
	return b.String()
}

// LogEntry es una linea de registro con su instante y las etiquetas del flujo del que salio.
type LogEntry struct {
	Timestamp time.Time         `json:"timestamp"`
	Line      string            `json:"line"`
	Labels    map[string]string `json:"labels"`
}

// LogPage es el resultado de una consulta con la ventana y el orden efectivos.
type LogPage struct {
	Service   string       `json:"service"`
	Since     time.Time    `json:"since"`
	Until     time.Time    `json:"until"`
	Direction LogDirection `json:"direction"`
	Limit     int          `json:"limit"`
	// Truncated: se alcanzo el limite y puede haber mas lineas en la ventana.
	Truncated bool       `json:"truncated"`
	Entries   []LogEntry `json:"entries"`
}

// SortEntries ordena las lineas segun la direccion y las recorta al limite: el almacen las devuelve por
// flujo y el orden global lo fija este servicio.
func SortEntries(entries []LogEntry, direction LogDirection, limit int) []LogEntry {
	sort.SliceStable(entries, func(i, j int) bool {
		if direction == DirectionForward {
			return entries[i].Timestamp.Before(entries[j].Timestamp)
		}
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries
}
