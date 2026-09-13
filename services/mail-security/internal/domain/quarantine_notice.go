package domain

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"html/template"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ── Enlaces sin sesion ────────────────────────────────────────────────────────

// QuarantineLinkAction es lo que ejecuta un enlace del aviso de cuarentena.
type QuarantineLinkAction string

const (
	LinkRelease QuarantineLinkAction = "release"
	LinkDiscard QuarantineLinkAction = "discard"
)

// QuarantineLinkBasePath es la raiz publica de los enlaces. El gateway declara debajo
// <celda>/<accion>, que enruta a la celda del segmento, y <accion> a secas, la forma de los
// enlaces emitidos antes de llevar la celda (routes.json, public).
const QuarantineLinkBasePath = "/api/v1/public/mail-security/quarantine"

// Valid dice si la accion existe.
func (a QuarantineLinkAction) Valid() bool {
	return a == LinkRelease || a == LinkDiscard
}

// Path devuelve la ruta publica del enlace en la celda: <base>/<celda>/<accion>.
func (a QuarantineLinkAction) Path(cell string) string {
	return QuarantineLinkBasePath + "/" + cell + "/" + string(a)
}

// minLinkKeyLen es el minimo de MAIL_LINK_SIGNING_KEY, el mismo que exige transactional.
const minLinkKeyLen = 32

// Codigo de celda: el mismo formato que admite organization al darla de alta.
var cellCodeRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

const maxCellCodeLen = 63

// QuarantineLinkClaims es lo que protege la firma junto con la celda del firmante:
// empresa, mensaje, accion y caducidad (segundos Unix). El qhash viaja en el enlace para
// encontrar la fila; la firma lo ata al mensaje porque la fila encontrada tiene que tener
// ese id.
type QuarantineLinkClaims struct {
	TenantID  uuid.UUID
	MessageID uuid.UUID
	Action    QuarantineLinkAction
	ExpiresAt int64
}

// QuarantineLinkSigner firma los enlaces de liberar y descartar con HMAC-SHA256 y
// MAIL_LINK_SIGNING_KEY. El mensaje canonico empieza por un dominio de separacion propio:
// la clave es la misma que firma las bajas de transactional y una firma de un tipo de
// enlace no vale para el otro. La firma va completa en hexadecimal y se compara en tiempo
// constante.
//
// Cada firmante es de UNA celda (CELL_CODE): firma con la suya y solo acepta la suya. La
// celda va en la ruta sin firmar para que el gateway enrute sin la clave, y va tambien en
// la firma: un segmento cambiado lleva el enlace a una celda que lo rechaza.
type QuarantineLinkSigner struct {
	key     []byte
	baseURL string
	cell    string
	ttl     time.Duration
}

func NewQuarantineLinkSigner(key, baseURL, cell string, ttl time.Duration) (*QuarantineLinkSigner, error) {
	if len(key) < minLinkKeyLen {
		return nil, errors.New("MAIL_LINK_SIGNING_KEY debe tener al menos 32 caracteres")
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	u, err := url.Parse(base)
	if base == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("PUBLIC_BASE_URL debe ser una URL absoluta http(s)")
	}
	if len(cell) > maxCellCodeLen || !cellCodeRe.MatchString(cell) {
		return nil, errors.New("CELL_CODE debe ser el codigo de la celda (minusculas, digitos y guiones)")
	}
	if ttl <= 0 {
		return nil, errors.New("MAIL_QUARANTINE_LINK_TTL debe ser positiva")
	}
	return &QuarantineLinkSigner{key: []byte(key), baseURL: base, cell: cell, ttl: ttl}, nil
}

// TTL es la vigencia de los enlaces que se emiten.
func (s *QuarantineLinkSigner) TTL() time.Duration { return s.ttl }

// Cell es la celda del firmante, la que va en la ruta y en la firma de sus enlaces.
func (s *QuarantineLinkSigner) Cell() string { return s.cell }

func (s *QuarantineLinkSigner) canonical(c QuarantineLinkClaims) []byte {
	return []byte("quarantine-link/v2\n" + s.cell + "\n" + c.TenantID.String() + "\n" + c.MessageID.String() + "\n" +
		string(c.Action) + "\n" + strconv.FormatInt(c.ExpiresAt, 10))
}

// legacyCanonical es la forma firmada de los enlaces emitidos antes de llevar la celda. Ya
// no se emite; se verifica mientras quede alguno vigente (como mucho MAIL_QUARANTINE_LINK_TTL
// desde el despliegue que la retiro). El dominio de separacion distinto impide que una firma
// de una forma valga por la otra.
func legacyCanonical(c QuarantineLinkClaims) []byte {
	return []byte("quarantine-link\n" + c.TenantID.String() + "\n" + c.MessageID.String() + "\n" +
		string(c.Action) + "\n" + strconv.FormatInt(c.ExpiresAt, 10))
}

func (s *QuarantineLinkSigner) mac(msg []byte) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write(msg)
	return hex.EncodeToString(mac.Sum(nil))
}

// Sign devuelve la firma hexadecimal de las claims en la celda del firmante.
func (s *QuarantineLinkSigner) Sign(c QuarantineLinkClaims) string {
	return s.mac(s.canonical(c))
}

// Verify comprueba que el enlace es de esta celda (cell es el segmento de la ruta), la
// accion, la caducidad y la firma. Cualquier fallo es false, sin distinguir.
func (s *QuarantineLinkSigner) Verify(cell string, c QuarantineLinkClaims, signature string, now time.Time) bool {
	if cell != s.cell || !c.Action.Valid() || c.ExpiresAt <= now.Unix() {
		return false
	}
	return equalSignature(s.Sign(c), signature)
}

// VerifyLegacy comprueba un enlace de la forma sin celda (ruta <base>/<accion>): accion,
// caducidad y firma. Solo lo recibe la celda por defecto del gateway, y solo encuentra el
// mensaje si la empresa esta en esta celda.
func (s *QuarantineLinkSigner) VerifyLegacy(c QuarantineLinkClaims, signature string, now time.Time) bool {
	if !c.Action.Valid() || c.ExpiresAt <= now.Unix() {
		return false
	}
	return equalSignature(s.mac(legacyCanonical(c)), signature)
}

func equalSignature(expected, signature string) bool {
	if len(signature) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1
}

// URL construye el enlace completo en la celda del firmante: t (empresa), q (qhash),
// e (caducidad) y sig.
func (s *QuarantineLinkSigner) URL(c QuarantineLinkClaims, qhash string) string {
	q := url.Values{}
	q.Set("t", c.TenantID.String())
	q.Set("q", qhash)
	q.Set("e", strconv.FormatInt(c.ExpiresAt, 10))
	q.Set("sig", s.Sign(c))
	return s.baseURL + c.Action.Path(s.cell) + "?" + q.Encode()
}

// IsHexToken dice si s tiene la forma de un qhash o de una firma: 64 caracteres
// hexadecimales en minusculas. Se comprueba antes de ir a la base.
func IsHexToken(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// QuarantineLinkUse es el registro del uso de un enlace. Una fila por mensaje: el primer
// enlace que se usa, de cualquiera de las dos acciones, consume los dos.
type QuarantineLinkUse struct {
	TenantID     uuid.UUID
	QuarantineID uuid.UUID
	Action       QuarantineLinkAction
	Rcpt         string
	ClientIP     string
	UserAgent    string
	UsedAt       time.Time
}

// ── Aviso al buzon ────────────────────────────────────────────────────────────

// NoticeStatus es como termino un aviso.
type NoticeStatus string

const (
	// NoticeSent: transactional acepto el aviso.
	NoticeSent NoticeStatus = "sent"
	// NoticeSuppressed: transactional lo acepto pero el buzon esta en la lista de supresion.
	NoticeSuppressed NoticeStatus = "suppressed"
	// NoticeRejected: transactional lo rechazo por una regla de negocio (4xx) o el HTML
	// renderizado no cabe; reintentar daria lo mismo.
	NoticeRejected NoticeStatus = "rejected"
	// NoticeSkipped: el buzon ya no existe o no recibe correo; no se envia nada.
	NoticeSkipped NoticeStatus = "skipped"
)

// QuarantineNotice es el registro de un aviso intentado. Se escribe en la misma
// transaccion que marca notified en los mensajes que lista.
type QuarantineNotice struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	Rcpt           string
	IdempotencyKey string
	Status         NoticeStatus
	MessageID      *uuid.UUID
	ErrorCode      string
	QuarantineIDs  []uuid.UUID
	CreatedAt      time.Time
}

// NoticeMail es el correo que se pide a transactional.
type NoticeMail struct {
	From           string
	To             string
	Subject        string
	HTML           string
	IdempotencyKey string
}

// NoticeGroup es el correo pendiente de aviso de un buzon, del mas reciente al mas antiguo.
type NoticeGroup struct {
	Mailbox  string
	Messages []QuarantineItem
}

// Latest es el mensaje mas reciente del grupo: su id forma la clave de idempotencia.
func (g NoticeGroup) Latest() QuarantineItem { return g.Messages[0] }

// IDs son los mensajes que lista el aviso, los que se marcan como avisados.
func (g NoticeGroup) IDs() []uuid.UUID {
	out := make([]uuid.UUID, len(g.Messages))
	for i, m := range g.Messages {
		out[i] = m.ID
	}
	return out
}

// GroupByMailbox agrupa los mensajes por buzon final, cada grupo del mas reciente al mas
// antiguo (a igualdad de fecha, por id descendente, como la consulta) y los grupos por
// buzon. No depende del orden de entrada.
func GroupByMailbox(items []QuarantineItem) []NoticeGroup {
	sorted := append([]QuarantineItem(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Rcpt != b.Rcpt {
			return a.Rcpt < b.Rcpt
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return strings.Compare(a.ID.String(), b.ID.String()) > 0
	})
	var out []NoticeGroup
	for _, it := range sorted {
		if n := len(out); n > 0 && out[n-1].Mailbox == it.Rcpt {
			out[n-1].Messages = append(out[n-1].Messages, it)
			continue
		}
		out = append(out, NoticeGroup{Mailbox: it.Rcpt, Messages: []QuarantineItem{it}})
	}
	return out
}

// maxIdempotencyKeyLen es el tope que acepta transactional para Idempotency-Key.
const maxIdempotencyKeyLen = 200

// NoticeIdempotencyKey es quarantine-notice:<buzon>:<id del mensaje mas reciente>. Mientras
// no llegue correo nuevo al buzon, repetir el aviso da la misma clave y transactional
// devuelve lo que ya creo. Un buzon tan largo que la clave pasaria del tope de
// transactional se sustituye por su sha256.
func NoticeIdempotencyKey(mailbox string, latest uuid.UUID) string {
	key := "quarantine-notice:" + mailbox + ":" + latest.String()
	if len(key) <= maxIdempotencyKeyLen {
		return key
	}
	sum := sha256.Sum256([]byte(mailbox))
	return "quarantine-notice:" + hex.EncodeToString(sum[:]) + ":" + latest.String()
}

// Topes del texto de terceros que entra en el aviso: un asunto de spam puede ocupar
// kilobytes y el aviso lista hasta un centenar de mensajes.
const (
	maxNoticeSubjectRunes = 200
	maxNoticeSenderRunes  = 254
)

// MaxNoticeHTMLBytes acota el HTML renderizado del aviso.
const MaxNoticeHTMLBytes = 1 << 20

// QuarantineNoticeEntry es un mensaje del aviso tal como lo ve la plantilla. Todo es texto
// plano: html/template lo escapa segun el contexto donde la plantilla lo coloque.
type QuarantineNoticeEntry struct {
	Subject    string
	Sender     string
	Date       string
	Score      string
	ReleaseURL string
	DiscardURL string
}

// QuarantineNoticeData es el contrato de notify_html_template (html/template de Go):
// {{.Mailbox}}, {{.Count}}, {{.LinksExpireAt}} y {{range .Messages}} con .Subject,
// .Sender, .Date, .Score, .ReleaseURL y .DiscardURL.
type QuarantineNoticeData struct {
	Mailbox       string
	Count         int
	LinksExpireAt string
	Messages      []QuarantineNoticeEntry
}

// noticeTimeLayout: fechas del aviso en UTC, legibles y sin ambiguedad.
const noticeTimeLayout = "2006-01-02 15:04 UTC"

// NewNoticeData arma los datos de la plantilla. El asunto y el remitente de un mensaje en
// cuarentena son contenido hostil: se sanean (UTF-8 valido, sin caracteres de control ni de
// direccion de texto, recortados) y el escapado lo pone html/template al renderizar.
func NewNoticeData(g NoticeGroup, expiresAt time.Time, link func(QuarantineItem, QuarantineLinkAction) string) QuarantineNoticeData {
	data := QuarantineNoticeData{
		Mailbox:       g.Mailbox,
		Count:         len(g.Messages),
		LinksExpireAt: expiresAt.UTC().Format(noticeTimeLayout),
		Messages:      make([]QuarantineNoticeEntry, len(g.Messages)),
	}
	for i, m := range g.Messages {
		data.Messages[i] = QuarantineNoticeEntry{
			Subject:    CleanThirdPartyText(m.Subject, maxNoticeSubjectRunes),
			Sender:     CleanThirdPartyText(m.Sender, maxNoticeSenderRunes),
			Date:       m.CreatedAt.UTC().Format(noticeTimeLayout),
			Score:      m.Score.StringFixed(2),
			ReleaseURL: link(m, LinkRelease),
			DiscardURL: link(m, LinkDiscard),
		}
	}
	return data
}

// CleanThirdPartyText deja texto de terceros apto para mostrarlo: UTF-8 valido, sin
// caracteres de control (un salto de linea en un asunto no es texto) ni los que invierten
// la direccion del texto (con ellos un remitente puede aparentar ser otro), y como mucho
// maxRunes runas. No escapa: eso depende del contexto y lo hace html/template.
func CleanThirdPartyText(s string, maxRunes int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) {
			r = ' '
		} else if isBidiControl(r) {
			continue
		}
		if n == maxRunes {
			return strings.TrimSpace(b.String()) + "..."
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

func isBidiControl(r rune) bool {
	return r == '\u061c' || r == '\u200e' || r == '\u200f' ||
		(r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069')
}

// NoticeTemplate es notify_html_template ya interpretado.
type NoticeTemplate struct {
	t *template.Template
}

// ParseNoticeTemplate interpreta la plantilla con html/template: todo lo que se inserte
// sale escapado segun el contexto (texto, atributo, URL), y la plantilla no tiene mas
// funciones que las de la biblioteca estandar.
func ParseNoticeTemplate(src string) (*NoticeTemplate, error) {
	if strings.TrimSpace(src) == "" {
		return nil, newValidation("notify.html_template es obligatoria con el aviso activo")
	}
	t, err := template.New("quarantine_notice").Parse(src)
	if err != nil {
		return nil, newValidation("notify.html_template no es una plantilla valida: " + err.Error())
	}
	return &NoticeTemplate{t: t}, nil
}

// ErrNoticeTooLarge: el HTML renderizado supera MaxNoticeHTMLBytes.
var ErrNoticeTooLarge = errors.New("el aviso renderizado supera el tamano maximo")

// Render ejecuta la plantilla. La salida se corta al superar MaxNoticeHTMLBytes.
func (t *NoticeTemplate) Render(data QuarantineNoticeData) (string, error) {
	w := &cappedBuffer{max: MaxNoticeHTMLBytes}
	if err := t.t.Execute(w, data); err != nil {
		if w.exceeded {
			return "", ErrNoticeTooLarge
		}
		return "", err
	}
	return w.buf.String(), nil
}

type cappedBuffer struct {
	buf      bytes.Buffer
	max      int
	exceeded bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.max {
		c.exceeded = true
		return 0, ErrNoticeTooLarge
	}
	return c.buf.Write(p)
}

// Topes de los campos del aviso en los ajustes.
const (
	maxNotifySubjectRunes = 255
	maxAddressLen         = 254
)

// ValidateQuarantineNotify comprueba un aviso activo: remitente con forma de direccion,
// asunto de una linea y plantilla que se interpreta y se ejecuta con datos vacios (asi un
// campo inexistente falla al guardar y no en cada barrido). Que el dominio del remitente
// este verificado para envio lo decide transactional al enviar.
func ValidateQuarantineNotify(n QuarantineNotify) error {
	sender := strings.TrimSpace(n.Sender)
	local, dom, ok := SplitAddress(sender)
	if !ok || len(sender) > maxAddressLen || strings.ContainsAny(local, " \t\r\n<>\",;") || ValidateDomainName(strings.ToLower(dom)) != nil {
		return newValidation("notify.sender debe ser una direccion de correo valida")
	}
	subject := strings.TrimSpace(n.Subject)
	if subject == "" || utf8.RuneCountInString(subject) > maxNotifySubjectRunes || strings.ContainsAny(subject, "\r\n") {
		return newValidation("notify.subject es obligatorio, de una linea y de hasta 255 caracteres")
	}
	tpl, err := ParseNoticeTemplate(n.HTMLTemplate)
	if err != nil {
		return err
	}
	probe := QuarantineNoticeData{Mailbox: sender, Count: 1, Messages: []QuarantineNoticeEntry{{}}}
	if _, err := tpl.Render(probe); err != nil {
		return newValidation("notify.html_template no se puede ejecutar: " + err.Error())
	}
	return nil
}
