package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/alonsosss/corforce-email/services/audit/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type AuditLogRepo struct{ pool *db.ContextPool }

func NewAuditLogRepo(pool *db.ContextPool) *AuditLogRepo { return &AuditLogRepo{pool: pool} }

// auditChainLockKey serializa la escritura de la cadena de hash dentro de la base
// de un tenant (cada empresa tiene su propia base, asi que el lock es por-empresa).
const auditChainLockKey = 4771001

// chainHash calcula el eslabon de esta fila: SHA-256 del hash anterior mas el
// contenido del evento TAL COMO LO ALMACENA la base. Es clave usar la forma
// almacenada: Postgres normaliza el jsonb (reordena/limpia) y guarda created_at con
// precision de microsegundos, asi que hashear el valor en memoria no cuadraria con
// lo que se lee al verificar. Por eso before/after/changes llegan como el texto del
// jsonb devuelto por la BD, y createdAt es el valor almacenado.
func chainHash(prev string, l *domain.AuditLog, before, after, changes string, createdAt time.Time) string {
	var b strings.Builder
	w := func(s string) { b.WriteString(s); b.WriteByte('|') }
	w(prev)
	w(l.ID.String())
	w(l.TenantID.String())
	w(l.UserID.String())
	if l.SessionID != nil {
		w(l.SessionID.String())
	} else {
		w("")
	}
	w(l.Action)
	w(l.Module)
	w(l.Resource)
	w(derefStr(l.ResourceID))
	w(l.IPAddress)
	w(derefStr(l.RequestID))
	w(before)
	w(after)
	w(changes)
	w(l.Severity)
	b.WriteString(createdAt.UTC().Format(time.RFC3339Nano))
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Create inserta la fila encadenada. No hay camino alternativo sin hash: la
// migracion canonica del esquema trae las columnas de la cadena desde el primer
// tenant, y una base que no las tenga debe fallar en vez de escribir una
// bitacora que el verificador no puede comprobar.
func (r *AuditLogRepo) Create(ctx context.Context, l *domain.AuditLog) error {
	tx, err := r.beginChained(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := appendChained(ctx, tx, l); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// beginChained abre la transaccion que escribe en la cadena y serializa a los
// escritores de este tenant: dos inserciones concurrentes no pueden leer el mismo
// "ultimo hash" y bifurcar la cadena. El candado es de transaccion, asi que se
// suelta solo al confirmar o revertir.
func (r *AuditLogRepo) beginChained(ctx context.Context) (pgx.Tx, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, auditChainLockKey); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// appendChained anade l al final de la cadena. Exige el candado de beginChained.
// Devuelve ErrLogAlreadyRecorded, sin escribir nada, si el id ya esta en la bitacora:
// es lo que hace idempotente una reentrega del bus.
func appendChained(ctx context.Context, tx pgx.Tx, l *domain.AuditLog) error {
	var prev string
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE((SELECT entry_hash FROM audit.audit_logs WHERE entry_hash IS NOT NULL ORDER BY seq DESC LIMIT 1),'')`,
	).Scan(&prev); err != nil {
		return err
	}

	// Se inserta y se devuelve la forma ALMACENADA (jsonb normalizado, created_at con
	// la precision real) para calcular el hash sobre exactamente eso.
	var storedCreated time.Time
	var storedBefore, storedAfter, storedChanges string
	err := tx.QueryRow(ctx,
		`INSERT INTO audit.audit_logs (id,tenant_id,user_id,session_id,action,module,resource,resource_id,ip_address,user_agent,request_id,before_data,after_data,changes,severity)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		 ON CONFLICT (id) DO NOTHING
		 RETURNING created_at, COALESCE(before_data::text,''), COALESCE(after_data::text,''), COALESCE(changes::text,'')`,
		l.ID, l.TenantID, l.UserID, l.SessionID, l.Action, l.Module, l.Resource, l.ResourceID, l.IPAddress, l.UserAgent, l.RequestID, l.Before, l.After, l.Changes, l.Severity).
		Scan(&storedCreated, &storedBefore, &storedAfter, &storedChanges)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrLogAlreadyRecorded
	}
	if err != nil {
		return err
	}

	entry := chainHash(prev, l, storedBefore, storedAfter, storedChanges, storedCreated)
	_, err = tx.Exec(ctx, `UPDATE audit.audit_logs SET prev_hash=$1, entry_hash=$2 WHERE id=$3`, prev, entry, l.ID)
	return err
}

// VerifyChain recorre la cadena por orden y detecta la primera fila cuyo hash no
// cuadra (contenido alterado) o cuyo prev_hash no enlaza con la anterior (fila
// borrada o insertada). Devuelve OK=true si la cadena esta intacta.
//
// Forman parte de la cadena las filas con seq o con entry_hash: solo las anteriores a
// la migracion 03 tienen ambos a NULL. Filtrar solo por entry_hash dejaria a quien
// escribe en la base borrar el hash de las ultimas filas y editarlas sin que nada lo
// note; una fila con seq y sin hash se lee con hash vacio y rompe la cadena.
func (r *AuditLogRepo) VerifyChain(ctx context.Context, tenantID uuid.UUID) (*domain.ChainIntegrity, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id,tenant_id,user_id,session_id,action,module,resource,resource_id,ip_address,request_id,severity,created_at,COALESCE(before_data::text,''),COALESCE(after_data::text,''),COALESCE(changes::text,''),COALESCE(prev_hash,''),COALESCE(entry_hash,'')
		   FROM audit.audit_logs WHERE seq IS NOT NULL OR entry_hash IS NOT NULL ORDER BY seq ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	res := &domain.ChainIntegrity{OK: true}
	expectedPrev := ""
	for rows.Next() {
		var l domain.AuditLog
		var before, after, changes, prevHash, entryHash string
		if err := rows.Scan(&l.ID, &l.TenantID, &l.UserID, &l.SessionID, &l.Action, &l.Module, &l.Resource, &l.ResourceID, &l.IPAddress, &l.RequestID, &l.Severity, &l.CreatedAt, &before, &after, &changes, &prevHash, &entryHash); err != nil {
			return nil, err
		}
		res.Checked++
		if prevHash != expectedPrev || chainHash(prevHash, &l, before, after, changes, l.CreatedAt) != entryHash {
			res.OK = false
			id := l.ID
			res.BrokenID = &id
			return res, nil
		}
		expectedPrev = entryHash
	}
	return res, rows.Err()
}

func (r *AuditLogRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*domain.AuditLog, error) {
	var l domain.AuditLog
	err := r.pool.QueryRow(ctx, `SELECT id,tenant_id,user_id,session_id,action,module,resource,resource_id,ip_address,user_agent,request_id,before_data,after_data,changes,severity,created_at FROM audit.audit_logs WHERE id=$1 AND tenant_id=$2`, id, tenantID).Scan(&l.ID, &l.TenantID, &l.UserID, &l.SessionID, &l.Action, &l.Module, &l.Resource, &l.ResourceID, &l.IPAddress, &l.UserAgent, &l.RequestID, &l.Before, &l.After, &l.Changes, &l.Severity, &l.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &l, err
}

func (r *AuditLogRepo) List(ctx context.Context, q domain.AuditQuery, page, pageSize int) ([]*domain.AuditLog, error) {
	where, args := buildAuditWhere(q)
	offset := (page - 1) * pageSize
	args = append(args, pageSize, offset)
	n := len(args)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT id,tenant_id,user_id,session_id,action,module,resource,resource_id,ip_address,user_agent,request_id,before_data,after_data,changes,severity,created_at FROM audit.audit_logs %s ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d`, where, n-1, n), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.AuditLog
	for rows.Next() {
		var l domain.AuditLog
		if err := rows.Scan(&l.ID, &l.TenantID, &l.UserID, &l.SessionID, &l.Action, &l.Module, &l.Resource, &l.ResourceID, &l.IPAddress, &l.UserAgent, &l.RequestID, &l.Before, &l.After, &l.Changes, &l.Severity, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &l)
	}
	return out, nil
}

func (r *AuditLogRepo) Count(ctx context.Context, q domain.AuditQuery) (int64, error) {
	where, args := buildAuditWhere(q)
	var n int64
	err := r.pool.QueryRow(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM audit.audit_logs %s`, where), args...).Scan(&n)
	return n, err
}

// BulkCreate anade el lote a la cadena, en una sola transaccion y con el candado tomado una
// vez. Las filas repetidas (mismo id) se omiten.
func (r *AuditLogRepo) BulkCreate(ctx context.Context, logs []*domain.AuditLog) error {
	tx, err := r.beginChained(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, l := range logs {
		if err := appendChained(ctx, tx, l); err != nil && !errors.Is(err, domain.ErrLogAlreadyRecorded) {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *AuditLogRepo) HasUserActionFromIP(ctx context.Context, tenantID, userID uuid.UUID, action, ip string, excludeID uuid.UUID) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM audit.audit_logs WHERE tenant_id=$1 AND user_id=$2 AND action=$3 AND ip_address=$4 AND id<>$5)`,
		tenantID, userID, action, ip, excludeID).Scan(&exists)
	return exists, err
}

func (r *AuditLogRepo) ListUserActionAgents(ctx context.Context, tenantID, userID uuid.UUID, action string, excludeID uuid.UUID, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT DISTINCT user_agent FROM audit.audit_logs
		  WHERE tenant_id=$1 AND user_id=$2 AND action=$3 AND id<>$4 AND user_agent IS NOT NULL AND user_agent <> ''
		  LIMIT $5`, tenantID, userID, action, excludeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ua string
		if err := rows.Scan(&ua); err != nil {
			return nil, err
		}
		out = append(out, ua)
	}
	return out, rows.Err()
}

func (r *AuditLogRepo) CountRecentByActionIP(ctx context.Context, tenantID uuid.UUID, action, ip string, since time.Time) (int64, error) {
	var n int64
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM audit.audit_logs WHERE tenant_id=$1 AND action=$2 AND ip_address=$3 AND created_at>=$4`,
		tenantID, action, ip, since).Scan(&n)
	return n, err
}

// RecentLoginOtherIP devuelve la IP del ultimo inicio de sesion del usuario desde
// una IP DISTINTA de la actual dentro de la ventana (vacio si no hay). Es la senal
// de viaje imposible: dos ubicaciones en minutos delatan una sesion paralela.
func (r *AuditLogRepo) RecentLoginOtherIP(ctx context.Context, tenantID, userID uuid.UUID, currentIP string, since time.Time, excludeID uuid.UUID) (string, error) {
	var ip string
	err := r.pool.QueryRow(ctx,
		`SELECT ip_address FROM audit.audit_logs
		   WHERE tenant_id=$1 AND user_id=$2 AND action='user.logged_in' AND ip_address IS NOT NULL AND ip_address<>'' AND ip_address<>$3 AND created_at>=$4 AND id<>$5
		   ORDER BY created_at DESC LIMIT 1`,
		tenantID, userID, currentIP, since, excludeID).Scan(&ip)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return ip, err
}

func buildAuditWhere(q domain.AuditQuery) (string, []interface{}) {
	conds := []string{"tenant_id=$1"}
	args := []interface{}{q.TenantID}
	n := 2
	if q.UserID != nil {
		conds = append(conds, fmt.Sprintf("user_id=$%d", n))
		args = append(args, *q.UserID)
		n++
	}
	if q.Module != nil {
		conds = append(conds, fmt.Sprintf("module=$%d", n))
		args = append(args, *q.Module)
		n++
	}
	if q.Resource != nil {
		conds = append(conds, fmt.Sprintf("resource=$%d", n))
		args = append(args, *q.Resource)
		n++
	}
	if q.Action != nil {
		conds = append(conds, fmt.Sprintf("action=$%d", n))
		args = append(args, *q.Action)
		n++
	}
	if q.Severity != nil {
		conds = append(conds, fmt.Sprintf("severity=$%d", n))
		args = append(args, *q.Severity)
		n++
	}
	if q.DateFrom != nil {
		conds = append(conds, fmt.Sprintf("created_at>=$%d", n))
		args = append(args, *q.DateFrom)
		n++
	}
	if q.DateTo != nil {
		conds = append(conds, fmt.Sprintf("created_at<=$%d", n))
		args = append(args, *q.DateTo)
		n++
	}
	if q.IPAddress != nil {
		conds = append(conds, fmt.Sprintf("ip_address=$%d", n))
		args = append(args, *q.IPAddress)
		n++
	}
	_ = n
	return "WHERE " + strings.Join(conds, " AND "), args
}

type SecurityEventRepo struct{ pool *db.ContextPool }

func NewSecurityEventRepo(pool *db.ContextPool) *SecurityEventRepo {
	return &SecurityEventRepo{pool: pool}
}

// host(ip_address): la columna es inet y el destino un string; sin la conversion
// explicita el driver falla al escanear y el listado entero devuelve error.
const secEvtCols = `id,tenant_id,user_id,event_type,host(ip_address),user_agent,detail,risk_level,acknowledged,acknowledged_by,acknowledged_at,created_at`

func (r *SecurityEventRepo) scanEvent(row pgx.Row) (*domain.SecurityEvent, error) {
	var e domain.SecurityEvent
	if err := row.Scan(&e.ID, &e.TenantID, &e.UserID, &e.EventType, &e.IPAddress, &e.UserAgent, &e.Detail, &e.RiskLevel, &e.Acknowledged, &e.AcknowledgedBy, &e.AcknowledgedAt, &e.CreatedAt); err != nil {
		return nil, err
	}
	return &e, nil
}

func (r *SecurityEventRepo) Create(ctx context.Context, e *domain.SecurityEvent) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO audit.security_events (id,tenant_id,user_id,event_type,ip_address,user_agent,detail,risk_level) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, e.ID, e.TenantID, e.UserID, e.EventType, e.IPAddress, e.UserAgent, e.Detail, e.RiskLevel)
	return err
}

func (r *SecurityEventRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*domain.SecurityEvent, error) {
	e, err := r.scanEvent(r.pool.QueryRow(ctx, `SELECT `+secEvtCols+` FROM audit.security_events WHERE id=$1 AND tenant_id=$2`, id, tenantID))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return e, err
}

func (r *SecurityEventRepo) List(ctx context.Context, tenantID uuid.UUID, f ports.SecurityFilters, page, pageSize int) ([]*domain.SecurityEvent, int64, error) {
	where, args := buildSecurityWhere(tenantID, f)
	var total int64
	if err := r.pool.QueryRow(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM audit.security_events %s`, where), args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	qArgs := append(args, pageSize, (page-1)*pageSize)
	n := len(qArgs)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT `+secEvtCols+` FROM audit.security_events %s ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d`, where, n-1, n), qArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*domain.SecurityEvent
	for rows.Next() {
		e, err := r.scanEvent(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, nil
}

func (r *SecurityEventRepo) Acknowledge(ctx context.Context, id, acknowledgedBy uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `UPDATE audit.security_events SET acknowledged=true,acknowledged_by=$2,acknowledged_at=now() WHERE id=$1 AND NOT acknowledged`, id, acknowledgedBy)
	return err
}

func (r *SecurityEventRepo) HasRecentEvent(ctx context.Context, tenantID uuid.UUID, eventType, ip string, since time.Time) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM audit.security_events
		  WHERE tenant_id=$1 AND event_type=$2 AND host(ip_address)=$3 AND created_at>=$4)`,
		tenantID, eventType, ip, since).Scan(&exists)
	return exists, err
}

func (r *SecurityEventRepo) GetUnacknowledged(ctx context.Context, tenantID uuid.UUID) ([]*domain.SecurityEvent, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+secEvtCols+` FROM audit.security_events WHERE tenant_id=$1 AND acknowledged=false ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.SecurityEvent
	for rows.Next() {
		e, err := r.scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func buildSecurityWhere(tenantID uuid.UUID, f ports.SecurityFilters) (string, []interface{}) {
	conds := []string{"tenant_id=$1"}
	args := []interface{}{tenantID}
	n := 2
	if f.EventType != nil {
		conds = append(conds, fmt.Sprintf("event_type=$%d", n))
		args = append(args, *f.EventType)
		n++
	}
	if f.RiskLevel != nil {
		conds = append(conds, fmt.Sprintf("risk_level=$%d", n))
		args = append(args, *f.RiskLevel)
		n++
	}
	if f.DateFrom != nil {
		conds = append(conds, fmt.Sprintf("created_at>=$%d", n))
		args = append(args, *f.DateFrom)
		n++
	}
	if f.DateTo != nil {
		conds = append(conds, fmt.Sprintf("created_at<=$%d", n))
		args = append(args, *f.DateTo)
		n++
	}
	if f.Acknowledged != nil {
		conds = append(conds, fmt.Sprintf("acknowledged=$%d", n))
		args = append(args, *f.Acknowledged)
		n++
	}
	_ = n
	return "WHERE " + strings.Join(conds, " AND "), args
}

type DataChangeRepo struct{ pool *db.ContextPool }

func NewDataChangeRepo(pool *db.ContextPool) *DataChangeRepo { return &DataChangeRepo{pool: pool} }

func (r *DataChangeRepo) CreateBatch(ctx context.Context, records []*domain.DataChangeRecord) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, rec := range records {
		if _, err := tx.Exec(ctx, `INSERT INTO audit.data_change_records (id,tenant_id,audit_log_id,field_name,old_value,new_value) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (id) DO NOTHING`, rec.ID, rec.TenantID, rec.AuditLogID, rec.FieldName, rec.OldValue, rec.NewValue); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *DataChangeRepo) GetByLogID(ctx context.Context, tenantID, logID uuid.UUID) ([]*domain.DataChangeRecord, error) {
	rows, err := r.pool.Query(ctx, `SELECT id,tenant_id,audit_log_id,field_name,old_value,new_value FROM audit.data_change_records WHERE tenant_id=$1 AND audit_log_id=$2`, tenantID, logID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.DataChangeRecord
	for rows.Next() {
		var rec domain.DataChangeRecord
		if err := rows.Scan(&rec.ID, &rec.TenantID, &rec.AuditLogID, &rec.FieldName, &rec.OldValue, &rec.NewValue); err != nil {
			return nil, err
		}
		out = append(out, &rec)
	}
	return out, nil
}

type AuditSummaryRepo struct{ pool *db.ContextPool }

func NewAuditSummaryRepo(pool *db.ContextPool) *AuditSummaryRepo {
	return &AuditSummaryRepo{pool: pool}
}

func (r *AuditSummaryRepo) GetByModule(ctx context.Context, tenantID uuid.UUID, dateFrom, dateTo time.Time) ([]*domain.AuditSummary, error) {
	rows, err := r.pool.Query(ctx, `SELECT module,COUNT(*),COUNT(DISTINCT user_id),MAX(created_at) FROM audit.audit_logs WHERE tenant_id=$1 AND created_at>=$2 AND created_at<=$3 GROUP BY module ORDER BY module`, tenantID, dateFrom, dateTo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.AuditSummary
	for rows.Next() {
		var s domain.AuditSummary
		if err := rows.Scan(&s.Module, &s.TotalActions, &s.UniqueUsers, &s.LastActivity); err != nil {
			return nil, err
		}
		out = append(out, &s)
	}
	return out, nil
}

func (r *AuditSummaryRepo) GetUserActivity(ctx context.Context, tenantID, userID uuid.UUID, dateFrom, dateTo time.Time) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM audit.audit_logs WHERE tenant_id=$1 AND user_id=$2 AND created_at>=$3 AND created_at<=$4`, tenantID, userID, dateFrom, dateTo).Scan(&count)
	return count, err
}
