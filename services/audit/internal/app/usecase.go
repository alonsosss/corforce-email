package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/alonsosss/corforce-email/services/audit/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type AuditDeps struct {
	Logs     ports.AuditLogRepository
	Security ports.SecurityEventRepository
	Changes  ports.DataChangeRepository
	Summary  ports.AuditSummaryRepository
	Events   ports.EventPublisher
	Logger   *zap.Logger

	Anchors      ports.ChainAnchorRepository
	AnchorEvents ports.ChainAnchorPublisher
	Tx           ports.Transactor

	// VerifyTimeout es el plazo de cada verificacion de cadena; cero toma DefaultVerifyTimeout.
	VerifyTimeout time.Duration
}

type AuditUseCase struct {
	logs     ports.AuditLogRepository
	security ports.SecurityEventRepository
	changes  ports.DataChangeRepository
	summary  ports.AuditSummaryRepository
	events   ports.EventPublisher
	logger   *zap.Logger

	anchors      ports.ChainAnchorRepository
	anchorEvents ports.ChainAnchorPublisher
	tx           ports.Transactor

	verifying     verificationGate
	verifyTimeout time.Duration
}

func NewAuditUseCase(deps AuditDeps) *AuditUseCase {
	verifyTimeout := deps.VerifyTimeout
	if verifyTimeout <= 0 {
		verifyTimeout = DefaultVerifyTimeout
	}
	return &AuditUseCase{
		logs:     deps.Logs,
		security: deps.Security,
		changes:  deps.Changes,
		summary:  deps.Summary,
		events:   deps.Events,
		logger:   deps.Logger,

		anchors:      deps.Anchors,
		anchorEvents: deps.AnchorEvents,
		tx:           deps.Tx,

		verifyTimeout: verifyTimeout,
	}
}

var securityActions = map[string]string{
	"login_failed":        "failed_login",
	"unauthorized_access": "unauthorized_access",
	"permission_changed":  "permission_change",
	"data_exported":       "data_export",
	"config_changed":      "config_change",
}

func (uc *AuditUseCase) LogAction(ctx context.Context, l *domain.AuditLog) error {
	if !isValidSeverity(l.Severity) {
		return domain.ErrInvalidSeverity
	}
	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	l.CreatedAt = time.Now().UTC()

	if err := uc.logs.Create(ctx, l); err != nil {
		if errors.Is(err, domain.ErrLogAlreadyRecorded) {
			return nil
		}
		return fmt.Errorf("create audit log: %w", err)
	}

	if eventType, ok := securityActions[l.Action]; ok {
		evt := &domain.SecurityEvent{
			ID:        uuid.New(),
			TenantID:  l.TenantID,
			UserID:    &l.UserID,
			EventType: eventType,
			IPAddress: l.IPAddress,
			UserAgent: l.UserAgent,
			Detail:    fmt.Sprintf("action=%s module=%s resource=%s", l.Action, l.Module, l.Resource),
			RiskLevel: severityToRisk(l.Severity),
			CreatedAt: time.Now().UTC(),
		}
		if err := uc.security.Create(ctx, evt); err != nil {
			uc.logger.Error("auto-create security event", zap.Error(err))
		}
		if err := uc.events.PublishSecurityAlert(l.TenantID.String(), eventType, evt.Detail, evt.RiskLevel, l.IPAddress, l.UserID.String()); err != nil {
			uc.logger.Error("publish security alert", zap.Error(err))
		}
	}
	return nil
}

func (uc *AuditUseCase) BulkLogActions(ctx context.Context, logs []*domain.AuditLog) error {
	now := time.Now().UTC()
	for _, l := range logs {
		if !isValidSeverity(l.Severity) {
			return domain.ErrInvalidSeverity
		}
		if l.ID == uuid.Nil {
			l.ID = uuid.New()
		}
		l.CreatedAt = now
	}
	return uc.logs.BulkCreate(ctx, logs)
}

func (uc *AuditUseCase) SearchAuditLogs(ctx context.Context, query domain.AuditQuery, page, pageSize int) ([]*domain.AuditLog, int64, error) {
	logs, err := uc.logs.List(ctx, query, page, pageSize)
	if err != nil {
		return nil, 0, err
	}
	total, err := uc.logs.Count(ctx, query)
	if err != nil {
		return nil, 0, err
	}
	return logs, total, nil
}

func (uc *AuditUseCase) GetAuditLog(ctx context.Context, id, tenantID uuid.UUID) (*domain.AuditLog, error) {
	l, err := uc.logs.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if l == nil {
		return nil, domain.ErrLogNotFound
	}
	return l, nil
}

func (uc *AuditUseCase) GetSecurityEvents(ctx context.Context, tenantID uuid.UUID, filters ports.SecurityFilters, page, pageSize int) ([]*domain.SecurityEvent, int64, error) {
	return uc.security.List(ctx, tenantID, filters, page, pageSize)
}

func (uc *AuditUseCase) GetSecurityEvent(ctx context.Context, id, tenantID uuid.UUID) (*domain.SecurityEvent, error) {
	evt, err := uc.security.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if evt == nil {
		return nil, domain.ErrSecurityEventNotFound
	}
	return evt, nil
}

func (uc *AuditUseCase) AcknowledgeSecurityEvent(ctx context.Context, id, tenantID, acknowledgedBy uuid.UUID) error {
	evt, err := uc.security.GetByID(ctx, id, tenantID)
	if err != nil {
		return err
	}
	if evt == nil {
		return domain.ErrSecurityEventNotFound
	}
	return uc.security.Acknowledge(ctx, id, acknowledgedBy)
}

func (uc *AuditUseCase) GetUnacknowledgedSecurityEvents(ctx context.Context, tenantID uuid.UUID) ([]*domain.SecurityEvent, error) {
	return uc.security.GetUnacknowledged(ctx, tenantID)
}

func (uc *AuditUseCase) GetAuditSummary(ctx context.Context, tenantID uuid.UUID, dateFrom, dateTo time.Time) ([]*domain.AuditSummary, error) {
	return uc.summary.GetByModule(ctx, tenantID, dateFrom, dateTo)
}

func (uc *AuditUseCase) GetUserActivityReport(ctx context.Context, tenantID, userID uuid.UUID, dateFrom, dateTo time.Time) (int64, error) {
	return uc.summary.GetUserActivity(ctx, tenantID, userID, dateFrom, dateTo)
}

func (uc *AuditUseCase) CompareChanges(ctx context.Context, tenantID, logID uuid.UUID, before, after string) error {
	var beforeMap, afterMap map[string]interface{}
	if err := json.Unmarshal([]byte(before), &beforeMap); err != nil {
		return fmt.Errorf("parse before JSON: %w", err)
	}
	if err := json.Unmarshal([]byte(after), &afterMap); err != nil {
		return fmt.Errorf("parse after JSON: %w", err)
	}

	allKeys := make(map[string]struct{})
	for k := range beforeMap {
		allKeys[k] = struct{}{}
	}
	for k := range afterMap {
		allKeys[k] = struct{}{}
	}

	var records []*domain.DataChangeRecord
	for key := range allKeys {
		oldVal := formatValue(beforeMap[key])
		newVal := formatValue(afterMap[key])
		if oldVal != newVal {
			records = append(records, &domain.DataChangeRecord{
				ID:         uuid.New(),
				TenantID:   tenantID,
				AuditLogID: logID,
				FieldName:  key,
				OldValue:   strPtr(oldVal),
				NewValue:   strPtr(newVal),
			})
		}
	}

	if len(records) > 0 {
		return uc.changes.CreateBatch(ctx, records)
	}
	return nil
}

func (uc *AuditUseCase) GetChanges(ctx context.Context, tenantID, logID uuid.UUID) ([]*domain.DataChangeRecord, error) {
	return uc.changes.GetByLogID(ctx, tenantID, logID)
}

func isValidSeverity(s string) bool {
	for _, v := range domain.ValidSeverities {
		if s == v {
			return true
		}
	}
	return false
}

func severityToRisk(severity string) string {
	switch severity {
	case "critical":
		return "critical"
	case "warning":
		return "medium"
	default:
		return "low"
	}
}

func formatValue(v interface{}) string {
	if v == nil {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
