package postgres

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/alonsosss/corforce-email/services/audit/internal/ports"
	"github.com/google/uuid"
)

func ptr[T any](v T) *T { return &v }

var (
	hashTime = time.Date(2026, 3, 4, 5, 6, 7, 123456000, time.UTC)
	hashLog  = domain.AuditLog{
		ID:         uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		TenantID:   uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		UserID:     uuid.MustParse("33333333-3333-3333-3333-333333333333"),
		SessionID:  ptr(uuid.MustParse("44444444-4444-4444-4444-444444444444")),
		Action:     "user.updated",
		Module:     "identity",
		Resource:   "users",
		ResourceID: ptr("u-1"),
		IPAddress:  "203.0.113.7",
		RequestID:  ptr("req-9"),
		Severity:   "warning",
	}
)

const (
	hashBefore  = `{"plan": "basic"}`
	hashAfter   = `{"plan": "pro"}`
	hashChanges = `{"plan": "pro"}`
)

// El formato del hash es un contrato con las filas ya escritas: cambiarlo hace que la
// cadena de toda empresa existente se lea como manipulada. Los vectores los calculo un
// SHA-256 aparte, fuera de Go, sobre "campo|campo|...|created_at RFC 3339 con nanosegundos
// sin ceros finales".
func TestElFormatoDelHashNoCambia(t *testing.T) {
	got := chainHash("prevhash0", &hashLog, hashBefore, hashAfter, hashChanges, hashTime)
	if want := "1c20429dc4ae0ad7553d47e992be81db9ca95e0d0309216b39e7128f18b45b74"; got != want {
		t.Fatalf("hash %s, se esperaba %s", got, want)
	}

	sparse := hashLog
	sparse.SessionID, sparse.ResourceID, sparse.RequestID = nil, nil, nil
	got = chainHash("prevhash0", &sparse, hashBefore, hashAfter, hashChanges, hashTime.Truncate(time.Second))
	if want := "110f2b09735a8cc611a154bf2e61d5220e539f5a34cc07ffd98ba2b362895b4a"; got != want {
		t.Fatalf("hash sin opcionales %s, se esperaba %s", got, want)
	}
}

// Cada campo que el hash cubre debe moverlo: un campo que no lo mueve se puede editar en la
// base sin que el verificador lo note.
func TestCadaCampoCubiertoAlteraElHash(t *testing.T) {
	base := chainHash("p", &hashLog, hashBefore, hashAfter, hashChanges, hashTime)
	mutate := map[string]func(l *domain.AuditLog){
		"id":          func(l *domain.AuditLog) { l.ID = uuid.New() },
		"tenant_id":   func(l *domain.AuditLog) { l.TenantID = uuid.New() },
		"user_id":     func(l *domain.AuditLog) { l.UserID = uuid.New() },
		"session_id":  func(l *domain.AuditLog) { l.SessionID = ptr(uuid.New()) },
		"sin sesion":  func(l *domain.AuditLog) { l.SessionID = nil },
		"action":      func(l *domain.AuditLog) { l.Action = "user.deleted" },
		"module":      func(l *domain.AuditLog) { l.Module = "billing" },
		"resource":    func(l *domain.AuditLog) { l.Resource = "roles" },
		"resource_id": func(l *domain.AuditLog) { l.ResourceID = ptr("u-2") },
		"ip_address":  func(l *domain.AuditLog) { l.IPAddress = "198.51.100.1" },
		"request_id":  func(l *domain.AuditLog) { l.RequestID = ptr("req-10") },
		"severity":    func(l *domain.AuditLog) { l.Severity = "critical" },
	}
	for name, fn := range mutate {
		l := hashLog
		fn(&l)
		if chainHash("p", &l, hashBefore, hashAfter, hashChanges, hashTime) == base {
			t.Errorf("cambiar %s no altera el hash", name)
		}
	}
	others := map[string]string{
		"prev_hash": chainHash("q", &hashLog, hashBefore, hashAfter, hashChanges, hashTime),
		"before":    chainHash("p", &hashLog, `{"plan": "x"}`, hashAfter, hashChanges, hashTime),
		"after":     chainHash("p", &hashLog, hashBefore, `{"plan": "x"}`, hashChanges, hashTime),
		"changes":   chainHash("p", &hashLog, hashBefore, hashAfter, `{"plan": "x"}`, hashTime),
		"created_at": chainHash("p", &hashLog, hashBefore, hashAfter, hashChanges,
			hashTime.Add(time.Microsecond)),
	}
	for name, h := range others {
		if h == base {
			t.Errorf("cambiar %s no altera el hash", name)
		}
	}
}

func TestElHashNoDependeDeLaZonaHorariaDeLaHora(t *testing.T) {
	lima := time.FixedZone("PET", -5*3600)
	a := chainHash("p", &hashLog, hashBefore, hashAfter, hashChanges, hashTime)
	b := chainHash("p", &hashLog, hashBefore, hashAfter, hashChanges, hashTime.In(lima))
	if a != b {
		t.Fatal("el mismo instante en otra zona da otro hash: la verificacion dependeria de la zona del servidor")
	}
}

func TestElHashEsHexadecimalDe256Bits(t *testing.T) {
	h := chainHash("", &hashLog, "", "", "", hashTime)
	if len(h) != 64 || strings.Trim(h, "0123456789abcdef") != "" {
		t.Fatalf("hash %q", h)
	}
}

func TestBuildAuditWhereSiempreAcotaPorEmpresa(t *testing.T) {
	tenant := uuid.New()
	where, args := buildAuditWhere(domain.AuditQuery{TenantID: tenant})
	if where != "WHERE tenant_id=$1" || !reflect.DeepEqual(args, []interface{}{tenant}) {
		t.Fatalf("%q %v", where, args)
	}
}

func TestBuildAuditWhereNumeraLosParametrosEnOrden(t *testing.T) {
	tenant, user := uuid.New(), uuid.New()
	from, to := time.Now().Add(-time.Hour), time.Now()
	where, args := buildAuditWhere(domain.AuditQuery{
		TenantID: tenant, UserID: &user, Module: ptr("billing"), Resource: ptr("invoices"),
		Action: ptr("invoice.created"), Severity: ptr("warning"), DateFrom: &from, DateTo: &to, IPAddress: ptr("203.0.113.9"),
	})
	want := "WHERE tenant_id=$1 AND user_id=$2 AND module=$3 AND resource=$4 AND action=$5 AND severity=$6 AND created_at>=$7 AND created_at<=$8 AND ip_address=$9"
	if where != want {
		t.Fatalf("%s", where)
	}
	if !reflect.DeepEqual(args, []interface{}{tenant, user, "billing", "invoices", "invoice.created", "warning", from, to, "203.0.113.9"}) {
		t.Fatalf("%v", args)
	}
}

func TestBuildAuditWhereNoInterpolaValoresDelCliente(t *testing.T) {
	hostile := "x' OR '1'='1"
	where, args := buildAuditWhere(domain.AuditQuery{TenantID: uuid.New(), Module: &hostile, Action: &hostile})
	if strings.Contains(where, hostile) || strings.Contains(where, "OR") {
		t.Fatalf("valor del cliente dentro del SQL: %s", where)
	}
	if len(args) != 3 || args[1] != hostile {
		t.Fatalf("%v", args)
	}
}

func TestBuildSecurityWhereNumeraLosParametrosEnOrden(t *testing.T) {
	tenant := uuid.New()
	from, to := time.Now().Add(-time.Hour), time.Now()
	where, args := buildSecurityWhere(tenant, ports.SecurityFilters{
		EventType: ptr("failed_login"), RiskLevel: ptr("high"), DateFrom: &from, DateTo: &to, Acknowledged: ptr(false),
	})
	want := "WHERE tenant_id=$1 AND event_type=$2 AND risk_level=$3 AND created_at>=$4 AND created_at<=$5 AND acknowledged=$6"
	if where != want || len(args) != 6 || args[5] != false {
		t.Fatalf("%s %v", where, args)
	}
	where, args = buildSecurityWhere(tenant, ports.SecurityFilters{})
	if where != "WHERE tenant_id=$1" || len(args) != 1 {
		t.Fatalf("%s %v", where, args)
	}
}
