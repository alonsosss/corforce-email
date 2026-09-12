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

// ChainIntegrity es el resultado de verificar la cadena de hash del rastro de
// auditoria. OK=false con BrokenID senala la primera fila manipulada o el punto
// donde se borro/inserto una fila.
type ChainIntegrity struct {
	OK       bool       `json:"ok"`
	Checked  int        `json:"checked"`
	BrokenID *uuid.UUID `json:"broken_id,omitempty"`
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
