package domain

import (
	"time"

	"github.com/google/uuid"
)

type AuditLog struct {
	ID         uuid.UUID  `json:"id"`
	TenantID   uuid.UUID  `json:"tenant_id"`
	UserID     uuid.UUID  `json:"user_id"`
	SessionID  *uuid.UUID `json:"session_id,omitempty"`
	Action     string     `json:"action"`
	Module     string     `json:"module"`
	Resource   string     `json:"resource"`
	ResourceID *string    `json:"resource_id,omitempty"`
	IPAddress  string     `json:"ip_address"`
	UserAgent  *string    `json:"user_agent,omitempty"`
	RequestID  *string    `json:"request_id,omitempty"`
	Before     *string    `json:"before,omitempty"`
	After      *string    `json:"after,omitempty"`
	Changes    *string    `json:"changes,omitempty"`
	Severity   string     `json:"severity"`
	CreatedAt  time.Time  `json:"created_at"`
}

// ChainName identifica cada cadena de hash de la base de una empresa.
type ChainName string

const (
	ChainAuditLogs      ChainName = "audit_logs"
	ChainSecurityEvents ChainName = "security_events"
)

// Codigos de rotura que reporta el verificador. Distinguen una cadena manipulada de una
// configuracion que impide verificarla: quien opera actua distinto en cada caso.
const (
	// ReasonChainBroken: el contenido de una fila o su enlace con la anterior no cuadran.
	ReasonChainBroken = "chain_broken"
	// ReasonHashKeyMissing: hay filas de hash version 2 y el servicio no tiene clave. No se
	// puede decir que esten bien.
	ReasonHashKeyMissing = "hash_key_missing"
	// ReasonHashKeyUnknown: la fila se firmo con una llave que el anillo ya no tiene.
	ReasonHashKeyUnknown = "hash_key_unknown"
	// ReasonHashVersionRegression: una fila de version 1 posterior a una de version 2.
	ReasonHashVersionRegression = "hash_version_regression"
	// ReasonHashVersionUnsupported: version que este servicio no conoce.
	ReasonHashVersionUnsupported = "hash_version_unsupported"
	// ReasonHeadBehindAnchor: la cabeza actual es anterior a un ancla ya publicada.
	ReasonHeadBehindAnchor = "head_behind_anchor"
	// ReasonAnchorMismatch: la posicion de un ancla contiene otro hash o ninguna fila.
	ReasonAnchorMismatch = "anchor_mismatch"
)

// ChainHead es la ultima fila de una cadena.
type ChainHead struct {
	Seq         int64  `json:"seq"`
	Hash        string `json:"hash"`
	HashVersion int    `json:"hash_version"`
}

// ChainAnchor es la cabeza de una cadena registrada en un momento. Solo se anade.
type ChainAnchor struct {
	ID          int64     `json:"-"`
	TenantID    uuid.UUID `json:"-"`
	Chain       ChainName `json:"-"`
	HeadSeq     int64     `json:"head_seq"`
	HeadHash    string    `json:"head_hash"`
	HashVersion int       `json:"hash_version"`
	AnchoredAt  time.Time `json:"anchored_at"`
}

// AnchorFindings es lo que el repositorio encuentra al contrastar una cadena con sus anclas.
type AnchorFindings struct {
	// Last es el ancla de posicion mas alta, nil si aun no hay ninguna.
	Last *ChainAnchor
	// Mismatched es el ancla de posicion mas baja cuya posicion no contiene su hash.
	Mismatched *ChainAnchor
}

// CheckAnchors devuelve el codigo de rotura que dejan las anclas, o "" si la cadena las
// contiene todas. head es nil si la cadena esta vacia. Una cabeza anterior al ancla mas alta
// es el borrado de las ultimas filas, que ninguna comprobacion de enlaces ve.
func CheckAnchors(head *ChainHead, f AnchorFindings) string {
	if f.Last != nil && (head == nil || head.Seq < f.Last.HeadSeq) {
		return ReasonHeadBehindAnchor
	}
	if f.Mismatched != nil {
		return ReasonAnchorMismatch
	}
	return ""
}

// ChainIntegrity es el resultado de verificar las cadenas de hash del rastro de auditoria.
// OK vale para el conjunto: las filas de audit_logs, las de security_events y las anclas.
// Cuando es falso, Chain dice cual fallo primero y Reason, BrokenID, BrokenSeq y BrokenVersion
// dicen donde y por que. Checked, Versions, Head y Anchor describen audit_logs; SecurityEvents
// es el resultado de la otra cadena, con la misma forma.
type ChainIntegrity struct {
	OK             bool            `json:"ok"`
	Checked        int             `json:"checked"`
	Chain          ChainName       `json:"chain"`
	Reason         string          `json:"reason,omitempty"`
	BrokenID       *uuid.UUID      `json:"broken_id,omitempty"`
	BrokenSeq      *int64          `json:"broken_seq,omitempty"`
	BrokenVersion  *int            `json:"broken_hash_version,omitempty"`
	Versions       map[string]int  `json:"versions,omitempty"`
	Head           *ChainHead      `json:"head,omitempty"`
	Anchor         *ChainAnchor    `json:"anchor,omitempty"`
	SecurityEvents *ChainIntegrity `json:"security_events,omitempty"`
}

type AuditQuery struct {
	TenantID  uuid.UUID
	UserID    *uuid.UUID
	Module    *string
	Resource  *string
	Action    *string
	Severity  *string
	DateFrom  *time.Time
	DateTo    *time.Time
	IPAddress *string
}

type AuditSummary struct {
	Module       string    `json:"module"`
	TotalActions int64     `json:"total_actions"`
	UniqueUsers  int64     `json:"unique_users"`
	LastActivity time.Time `json:"last_activity"`
}

type SecurityEvent struct {
	ID             uuid.UUID  `json:"id"`
	TenantID       uuid.UUID  `json:"tenant_id"`
	UserID         *uuid.UUID `json:"user_id,omitempty"`
	EventType      string     `json:"event_type"`
	IPAddress      string     `json:"ip_address"`
	UserAgent      *string    `json:"user_agent,omitempty"`
	Detail         string     `json:"detail"`
	RiskLevel      string     `json:"risk_level"`
	Acknowledged   bool       `json:"acknowledged"`
	AcknowledgedBy *uuid.UUID `json:"acknowledged_by,omitempty"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

type DataChangeRecord struct {
	ID         uuid.UUID `json:"id"`
	TenantID   uuid.UUID `json:"tenant_id"`
	AuditLogID uuid.UUID `json:"audit_log_id"`
	FieldName  string    `json:"field_name"`
	OldValue   *string   `json:"old_value,omitempty"`
	NewValue   *string   `json:"new_value,omitempty"`
}

var ValidSeverities = []string{"info", "warning", "critical"}

var ValidEventTypes = []string{
	"failed_login", "unauthorized_access", "permission_change",
	"data_export", "config_change", "suspicious_activity",
}

var ValidRiskLevels = []string{"low", "medium", "high", "critical"}
