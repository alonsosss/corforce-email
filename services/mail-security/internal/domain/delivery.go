package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Registro de entregas del correo corporativo (docs/Plan_Registro_de_Envios.md, seccion 2): lo
// que Postfix entrega, rebota, aplaza o rechaza, por empresa y con el motivo del otro servidor.

// Estados de un evento de entrega. Los cuatro primeros son los status= de Postfix; rejected es un
// rechazo antes de entrar en la cola (milter o smtpd).
const (
	DeliverySent     = "sent"
	DeliveryBounced  = "bounced"
	DeliveryDeferred = "deferred"
	DeliveryExpired  = "expired"
	DeliveryRejected = "rejected"
)

func DeliveryStatuses() []string {
	return []string{DeliverySent, DeliveryBounced, DeliveryDeferred, DeliveryExpired, DeliveryRejected}
}

// Direccion del correo respecto de la empresa: outbound lo envio un buzon suyo autenticado,
// inbound iba dirigido a uno de sus dominios.
const (
	DirectionOutbound = "outbound"
	DirectionInbound  = "inbound"
)

func DeliveryDirections() []string { return []string{DirectionOutbound, DirectionInbound} }

// Topes de lo que se guarda de una linea: una respuesta de otro servidor puede ser larga y no es
// de fiar.
const (
	MaxDeliveryReason  = 1000
	MaxDeliveryAddress = 320
	MaxDeliveryField   = 255
)

var ErrMailLogLine = errors.New("línea de registro de Postfix no válida")

// MailLogLine es una linea del registro de Postfix tal como la escribe syslog-ng en Redis:
// {"time":"<unix>","program":"postfix/smtp","priority":"info","message":"..."}.
type MailLogLine struct {
	Time    time.Time
	Program string
	Message string
}

// ParseMailLogLine lee la linea JSON de syslog-ng.
func ParseMailLogLine(raw string) (MailLogLine, error) {
	var in struct {
		Time    string `json:"time"`
		Program string `json:"program"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return MailLogLine{}, ErrMailLogLine
	}
	secs, err := strconv.ParseInt(strings.TrimSpace(in.Time), 10, 64)
	if err != nil || secs <= 0 || in.Message == "" {
		return MailLogLine{}, ErrMailLogLine
	}
	return MailLogLine{Time: time.Unix(secs, 0).UTC(), Program: in.Program, Message: in.Message}, nil
}

// Clases de linea que interesan al registro. El resto (postscreen, anvil, proxymap...) se ignora.
const (
	LogClient = iota + 1
	LogMessageID
	LogFrom
	LogDelivery
	LogReject
)

// LogRecord es lo que una linea aporta. QID vacio solo en un rechazo previo a la cola
// (NOQUEUE).
type LogRecord struct {
	Kind         int
	QID          string
	SASLUsername string
	ClientIP     string
	MessageID    string
	From         string
	To           string
	OrigTo       string
	Relay        string
	DSN          string
	Status       string
	Reason       string
	Delay        string
	// Local indica una entrega en un buzon de la celda (Dovecot por LMTP), no a otro servidor.
	Local bool
}

var (
	// Id de cola corto (hexadecimal) o largo (enable_long_queue_ids).
	reQID       = regexp.MustCompile(`^([0-9A-F]{6,12}|[0-9B-DF-HJ-NP-TV-Zb-df-hj-np-tv-z]{11,20}): (.*)$`)
	reClient    = regexp.MustCompile(`^client=[^\[,]*\[([0-9A-Fa-f.:]+)\]`)
	reSASL      = regexp.MustCompile(`sasl_username=([^,\s]+)`)
	reMessageID = regexp.MustCompile(`^message-id=<?([^>\s]*)>?$`)
	reFrom      = regexp.MustCompile(`^from=<([^>]*)>, size=`)
	reDelivery  = regexp.MustCompile(`^to=<([^>]*)>, (?:orig_to=<([^>]*)>, )?relay=([^,]+), (?:conn_use=\d+, )?delay=([0-9.]+), delays=[^,]+, dsn=([0-9.]+), status=([a-z]+) \((.*)\)$`)
	// milter-reject (el antispam) y reject de header_checks/body_checks, con id de cola.
	reQueuedReject = regexp.MustCompile(`^(?:milter-reject|reject): (?:\S+ )?(?:from \S*?\[[^\]]*\](?::\d+)?: )?(.*?); from=<([^>]*)>,? to=<([^>]*)>`)
	// Rechazo antes de la cola: smtpd y postscreen (este separa from y to con comas).
	reDelay   = regexp.MustCompile(`^[0-9]{1,9}(\.[0-9]{1,3})?$`)
	reNoQueue = regexp.MustCompile(`^NOQUEUE: reject: \S+ from \S*?\[[^\]]*\](?::\d+)?: (.*?); from=<([^>]*)>,? to=<([^>]*)>`)
)

// ClassifyLogLine extrae de la linea lo que aporta al registro; false si no aporta nada.
func ClassifyLogLine(l MailLogLine) (LogRecord, bool) {
	if !strings.HasPrefix(l.Program, "postfix/") {
		return LogRecord{}, false
	}
	msg := strings.TrimSpace(l.Message)
	if m := reNoQueue.FindStringSubmatch(msg); m != nil {
		return LogRecord{Kind: LogReject, Reason: m[1], From: m[2], To: m[3], Status: DeliveryRejected}, true
	}
	m := reQID.FindStringSubmatch(msg)
	if m == nil {
		return LogRecord{}, false
	}
	qid, rest := m[1], m[2]
	switch {
	case strings.HasPrefix(rest, "client="):
		rec := LogRecord{Kind: LogClient, QID: qid}
		if c := reClient.FindStringSubmatch(rest); c != nil {
			rec.ClientIP = c[1]
		}
		if s := reSASL.FindStringSubmatch(rest); s != nil {
			rec.SASLUsername = strings.ToLower(s[1])
		}
		return rec, true
	case strings.HasPrefix(rest, "message-id="):
		if id := reMessageID.FindStringSubmatch(rest); id != nil {
			return LogRecord{Kind: LogMessageID, QID: qid, MessageID: id[1]}, true
		}
	case strings.HasPrefix(rest, "from=<"):
		if f := reFrom.FindStringSubmatch(rest); f != nil {
			return LogRecord{Kind: LogFrom, QID: qid, From: f[1]}, true
		}
	case strings.HasPrefix(rest, "to=<"):
		d := reDelivery.FindStringSubmatch(rest)
		if d == nil || !validDeliveryStatus(d[6]) {
			return LogRecord{}, false
		}
		return LogRecord{
			Kind: LogDelivery, QID: qid, To: d[1], OrigTo: d[2], Relay: d[3], Delay: d[4], DSN: d[5],
			Status: d[6], Reason: d[7], Local: isLocalDelivery(l.Program, d[3]),
		}, true
	default:
		if r := reQueuedReject.FindStringSubmatch(rest); r != nil {
			return LogRecord{Kind: LogReject, QID: qid, Reason: r[1], From: r[2], To: r[3], Status: DeliveryRejected}, true
		}
	}
	return LogRecord{}, false
}

func validDeliveryStatus(s string) bool {
	switch s {
	case DeliverySent, DeliveryBounced, DeliveryDeferred, DeliveryExpired:
		return true
	}
	return false
}

// isLocalDelivery: la entrega en un buzon de la celda la hace Postfix por LMTP a Dovecot.
func isLocalDelivery(program, relay string) bool {
	return strings.HasSuffix(program, "/lmtp") || strings.HasPrefix(relay, "dovecot") ||
		strings.HasSuffix(program, "/virtual") || strings.HasSuffix(program, "/local")
}

// QueueContext es lo que se sabe de un mensaje en cola antes de su entrega, reunido de las lineas
// client=, message-id= y from= de su id de cola.
type QueueContext struct {
	SASLUsername string `json:"sasl,omitempty"`
	ClientIP     string `json:"ip,omitempty"`
	MessageID    string `json:"mid,omitempty"`
	From         string `json:"from,omitempty"`
}

// DeliveryEvent es una fila del registro de entregas de una empresa.
type DeliveryEvent struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	Direction    string
	QueueID      string
	MessageID    string
	Sender       string
	Recipient    string
	Status       string
	DSN          string
	Relay        string
	Reason       string
	DelaySeconds string
	SASLUsername string
	ClientIP     string
	OccurredAt   time.Time
	// EventKey es la huella de la linea de origen y la empresa: la misma linea procesada dos veces
	// no crea una segunda fila.
	EventKey  string
	CreatedAt time.Time
}

// NewDeliveryEvent arma el evento de una empresa a partir de la linea y lo que se sabe de su
// mensaje, recortando lo que viene de fuera.
func NewDeliveryEvent(tenantID uuid.UUID, direction string, rec LogRecord, qc QueueContext, line MailLogLine, raw string) DeliveryEvent {
	sender := rec.From
	if sender == "" {
		sender = qc.From
	}
	sum := sha256.Sum256([]byte(tenantID.String() + "|" + direction + "|" + raw))
	// Solo digitos: strconv.ParseFloat admite NaN e Inf, que ni son una espera ni se serializan.
	delay := rec.Delay
	if !reDelay.MatchString(delay) {
		delay = ""
	}
	return DeliveryEvent{
		ID: uuid.New(), TenantID: tenantID, Direction: direction,
		QueueID:      clip(rec.QID, MaxDeliveryField),
		MessageID:    clip(qc.MessageID, MaxDeliveryField),
		Sender:       clip(strings.ToLower(sender), MaxDeliveryAddress),
		Recipient:    clip(strings.ToLower(rec.To), MaxDeliveryAddress),
		Status:       rec.Status,
		DSN:          clip(rec.DSN, 16),
		Relay:        clip(rec.Relay, MaxDeliveryField),
		Reason:       clip(rec.Reason, MaxDeliveryReason),
		DelaySeconds: delay,
		SASLUsername: clip(qc.SASLUsername, MaxDeliveryAddress),
		ClientIP:     clip(qc.ClientIP, 64),
		OccurredAt:   line.Time,
		EventKey:     hex.EncodeToString(sum[:]),
	}
}

// AddressDomain devuelve el dominio en minusculas de una direccion, vacio si no tiene.
func AddressDomain(address string) string {
	at := strings.LastIndexByte(address, '@')
	if at < 0 || at == len(address)-1 {
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(address[at+1:], "."))
}

// clip recorta a max bytes sin partir un caracter y quita los de control.
func clip(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// DeliveryFilter es la consulta del registro de una empresa.
type DeliveryFilter struct {
	Direction string
	Status    string
	// Address busca en remitente y destinatario (coincidencia exacta, en minusculas).
	Address  string
	DateFrom *time.Time
	DateTo   *time.Time
	Page     int
	PerPage  int
}

// Paginacion del registro de entregas.
const (
	DefaultDeliveryPerPage = 50
	MaxDeliveryPerPage     = 200
)

// Normalize acota la paginacion: pagina 1 y 50 por pagina por defecto, 200 como mucho.
func (f *DeliveryFilter) Normalize() {
	f.Page = max(f.Page, 1)
	if f.PerPage < 1 {
		f.PerPage = DefaultDeliveryPerPage
	}
	f.PerPage = min(f.PerPage, MaxDeliveryPerPage)
}
