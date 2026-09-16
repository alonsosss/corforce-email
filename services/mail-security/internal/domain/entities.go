package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ObjectKind distingue si un objeto de politica es un buzon o un dominio. Determina
// como se traduce a una condicion de Rspamd (rcpt exacto o sufijo @dominio).
type ObjectKind string

const (
	ObjectMailbox ObjectKind = "mailbox"
	ObjectDomain  ObjectKind = "domain"
)

// ListKind es el sentido de una entrada de lista de direcciones.
type ListKind string

const (
	ListAllow ListKind = "allow"
	ListDeny  ListKind = "deny"
)

// SpamScore fija los umbrales de rechazo (high) y marcado (low) de un objeto.
type SpamScore struct {
	ID        uuid.UUID       `json:"id"`
	TenantID  uuid.UUID       `json:"tenant_id"`
	Object    string          `json:"object"`
	HighScore decimal.Decimal `json:"high_score"`
	LowScore  decimal.Decimal `json:"low_score"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// AddressListEntry es una entrada de lista blanca o negra de remitentes para un objeto.
type AddressListEntry struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	Object    string    `json:"object"`
	Kind      ListKind  `json:"kind"`
	Pattern   string    `json:"pattern"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SettingsMap es un bloque UCL adicional que se pega dentro de settings { } de Rspamd.
type SettingsMap struct {
	ID          uuid.UUID `json:"id"`
	TenantID    uuid.UUID `json:"tenant_id"`
	Description string    `json:"description"`
	Content     string    `json:"content"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// DomainFooter es el pie de pagina que Rspamd anade al correo saliente de un dominio.
type DomainFooter struct {
	ID                 uuid.UUID `json:"id"`
	TenantID           uuid.UUID `json:"tenant_id"`
	Domain             string    `json:"domain"`
	HTML               string    `json:"html"`
	Plain              string    `json:"plain"`
	MailboxExclude     []string  `json:"mailbox_exclude"`
	AliasDomainExclude []string  `json:"alias_domain_exclude"`
	SkipReplies        bool      `json:"skip_replies"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// ForwardingHost es un host (IP o CIDR) de reenvio de confianza.
type ForwardingHost struct {
	ID         uuid.UUID `json:"id"`
	TenantID   uuid.UUID `json:"tenant_id"`
	Host       string    `json:"host"`
	Source     string    `json:"source"`
	FilterSpam bool      `json:"filter_spam"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// RateLimit limita los envios de un buzon o dominio: value con formato "N / 1h".
type RateLimit struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	Object    string    `json:"object"`
	Value     string    `json:"value"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MailboxTags dice como entregar correo dirigido a local+tag@dominio para un buzon.
type MailboxTags struct {
	ID           uuid.UUID `json:"id"`
	TenantID     uuid.UUID `json:"tenant_id"`
	Username     string    `json:"username"`
	SubjectTag   bool      `json:"subject_tag"`
	SubfolderTag bool      `json:"subfolder_tag"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// QuarantineItem es un mensaje retenido para un buzon final. Msg solo se carga cuando
// se pide el mensaje completo; en listados y detalle viaja vacio.
type QuarantineItem struct {
	ID          uuid.UUID       `json:"id"`
	TenantID    uuid.UUID       `json:"tenant_id"`
	QID         string          `json:"qid"`
	Subject     string          `json:"subject"`
	Score       decimal.Decimal `json:"score"`
	IP          string          `json:"ip"`
	Action      string          `json:"action"`
	Symbols     []string        `json:"symbols"`
	FuzzyHashes []string        `json:"fuzzy_hashes"`
	Sender      string          `json:"sender"`
	Rcpt        string          `json:"rcpt"`
	Domain      string          `json:"domain"`
	Notified    bool            `json:"notified"`
	UserName    string          `json:"user_name"`
	QHash       string          `json:"qhash"`
	Size        int             `json:"size"`
	CreatedAt   time.Time       `json:"created_at"`
	Msg         []byte          `json:"-"`
}

// QuarantineFilter acota un listado de cuarentena.
type QuarantineFilter struct {
	Rcpt     string
	ScoreMin *decimal.Decimal
	Page     int
	PerPage  int
}

// Paginacion del listado de cuarentena: la pagina por defecto y la mayor que se sirve. El tope
// vive aqui porque tambien acota la retencion (MaxQuarantineRetentionSize).
const (
	DefaultQuarantinePerPage = 50
	MaxQuarantinePerPage     = 200
)

// QuarantineNotify configura el aviso de cuarentena al usuario.
type QuarantineNotify struct {
	Enabled      bool            `json:"enabled"`
	MaxScore     decimal.Decimal `json:"max_score"`
	Sender       string          `json:"sender"`
	Subject      string          `json:"subject"`
	HTMLTemplate string          `json:"html_template"`
}

// QuarantineSettings son los ajustes de cuarentena de una empresa.
type QuarantineSettings struct {
	TenantID       uuid.UUID        `json:"tenant_id"`
	MaxSizeBytes   int64            `json:"max_size_bytes"`
	MaxAgeDays     int              `json:"max_age_days"`
	RetentionSize  int              `json:"retention_size"`
	ExcludeDomains []string         `json:"exclude_domains"`
	Notify         QuarantineNotify `json:"notify"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

// Valores por defecto cuando la empresa no ha fijado ajustes: los mismos limites que
// traian los motores copiados (10 MiB, un ano, 100 mensajes por buzon).
const (
	DefaultQuarantineMaxSizeBytes  int64 = 10 * 1024 * 1024
	DefaultQuarantineMaxAgeDays          = 365
	DefaultQuarantineRetentionSize       = 100
)

// MaxQuarantineMaxSizeBytes es el mayor max_size_bytes que puede pedir una empresa: el
// mayor mensaje que /pipe llega a recibir (maxPipeMaxBodyMiB de services/mail-security/
// main.go, message_size_limit de Postfix mas 1 MiB de envoltorio). Un ajuste por encima no
// guardaria ni un mensaje mas y taparia el motivo real de que algo no quede en cuarentena.
// La migracion 08 repite el numero como CHECK y ops/scaffold/check-mail-size-limits.sh ata
// los tres.
const MaxQuarantineMaxSizeBytes int64 = 101 * 1024 * 1024

// Techos de la retencion de cuarentena. Los tres ajustes viajan ademas a Redis como el TOPE de la
// CELDA (el maximo entre empresas y la union de dominios, ComputeCellQuarantineTop): sin techo, lo
// que pide una empresa se lo lleva toda la celda.
const (
	// MaxQuarantineRetentionSize son las filas que la celda guarda por buzon. No es un numero
	// redondo sino diez paginas del unico listado que sirve esas filas (MaxQuarantinePerPage), que
	// son tambien veinte avisos de cuarentena (maxMessagesPerNotice, 100): pasado eso los mensajes
	// ya no se revisan, solo se guardan, y cada fila cuesta dos veces. En la base, porque lleva el
	// mensaje ENTERO (quarantine.msg): al tamano por defecto (DefaultQuarantineMaxSizeBytes, 10
	// MiB) son casi 20 GiB de UN solo buzon en la base compartida de la celda, y al techo por
	// mensaje casi 200. Y en la entrega, porque la poda por buzon corre dentro de /pipe, que Rspamd
	// llama con cada mensaje que va a cuarentena, y su DELETE recorre retention_size entradas del
	// indice antes de dar con la primera que sobra.
	MaxQuarantineRetentionSize = 10 * MaxQuarantinePerPage
	// MaxQuarantineMaxAgeDays es el doble del defecto: dos anos. La cuarentena guarda el cuerpo
	// entero de correo de terceros, spam y malware incluidos, y PruneAged es lo unico que retira el
	// mensaje que nadie llega a revisar; sin techo una empresa podia pedir una retencion
	// practicamente infinita y dejar ese contenido en la celda para siempre.
	MaxQuarantineMaxAgeDays = 2 * DefaultQuarantineMaxAgeDays
	// MaxQuarantineExcludeDomains acota la lista de dominios excluidos, que viaja entera a Redis
	// como UN valor (Q_EXCLUDE_DOMAINS) unido al de las demas empresas de la celda. Son muchos mas
	// dominios de los que una empresa gestiona; el limite esta para que una lista sin fin no hinche
	// una clave que leen los motores de todas.
	MaxQuarantineExcludeDomains = 256
)

// DefaultQuarantineSettings devuelve los ajustes que rigen sin fila propia.
func DefaultQuarantineSettings(tenantID uuid.UUID) QuarantineSettings {
	return QuarantineSettings{
		TenantID:       tenantID,
		MaxSizeBytes:   DefaultQuarantineMaxSizeBytes,
		MaxAgeDays:     DefaultQuarantineMaxAgeDays,
		RetentionSize:  DefaultQuarantineRetentionSize,
		ExcludeDomains: []string{},
		Notify:         QuarantineNotify{MaxScore: decimal.NewFromInt(9999)},
	}
}

// ExcludesDomain indica si los buzones de ese dominio no se guardan en cuarentena.
func (s QuarantineSettings) ExcludesDomain(domain string) bool {
	for _, d := range s.ExcludeDomains {
		if d == domain {
			return true
		}
	}
	return false
}

// Mailbox es la proyeccion del directorio que este servicio necesita de un buzon.
type Mailbox struct {
	TenantID uuid.UUID
	Username string
	Domain   string
	Active   int16
	Kind     string
}

// Receives indica si el buzon acepta correo: activo (1) o solo recepcion (2), y no es
// un recurso (sala, equipo, grupo), que Postfix envia a null@localhost.
func (m Mailbox) Receives() bool {
	return (m.Active == 1 || m.Active == 2) && m.Kind == ""
}

// PolicyObject resuelve a que empresa pertenece un objeto y de que tipo es.
type PolicyObject struct {
	TenantID uuid.UUID
	Object   string
	Kind     ObjectKind
}

// QuarantineMetadata es lo que el exportador de Rspamd envia en /pipe.
type QuarantineMetadata struct {
	QID       string          `json:"qid"`
	Subject   string          `json:"subject"`
	Score     decimal.Decimal `json:"score"`
	Rcpt      []string        `json:"rcpt"`
	User      string          `json:"user"`
	IP        string          `json:"ip"`
	Action    string          `json:"action"`
	From      string          `json:"from"`
	Symbols   []string        `json:"symbols"`
	Fuzzy     []string        `json:"fuzzy"`
	MessageID string          `json:"message_id"`
}

// RateLimitLog es la linea que se apila en RL_LOG cuando Rspamd limita un envio.
type RateLimitLog struct {
	Time          int64    `json:"time"`
	Rcpt          []string `json:"rcpt"`
	From          string   `json:"from"`
	User          string   `json:"user"`
	RLInfo        string   `json:"rl_info"`
	RLName        string   `json:"rl_name"`
	RLHash        string   `json:"rl_hash"`
	QID           string   `json:"qid"`
	IP            string   `json:"ip"`
	MessageID     string   `json:"message_id"`
	HeaderSubject []string `json:"header_subject"`
	HeaderFrom    []string `json:"header_from"`
}

// FooterResponse es lo que espera el simbolo MOO_FOOTER de Rspamd. Vars viaja como
// cadena JSON y no como objeto porque el Lua solo la interpreta si type(vars) == "string".
type FooterResponse struct {
	HTML        string `json:"html"`
	Plain       string `json:"plain"`
	SkipReplies int    `json:"skip_replies"`
	Vars        string `json:"vars"`
}

// DKIMKey es la clave que domain-service entrega para publicarla en Redis. Nunca se
// guarda en la base de este servicio.
type DKIMKey struct {
	Domain        string
	Selector      string
	PrivateKeyPEM string
}

// InternalAlias es un alias activo marcado como interno en el directorio.
type InternalAlias struct {
	Address string
	Domain  string
}
