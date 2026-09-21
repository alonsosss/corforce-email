//go:build integration

package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/audit/internal/app"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/alonsosss/corforce-email/services/audit/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// otherTenant es otra empresa en la MISMA base: el servicio no debe mezclarlas aunque un
// bug de enrutado pusiera sus filas juntas (cada empresa tiene su propia base, esto es la
// segunda barrera: tenant_id en cada consulta).
func (e *env) otherTenant() *env {
	o := *e
	o.tenant, o.user = uuid.New(), uuid.New()
	return &o
}

func idSet(logs []*domain.AuditLog) map[uuid.UUID]bool {
	out := map[uuid.UUID]bool{}
	for _, l := range logs {
		out[l.ID] = true
	}
	return out
}

func (e *env) list(t *testing.T, q domain.AuditQuery, page, size int) []*domain.AuditLog {
	t.Helper()
	out, err := e.logs.List(e.ctx, q, page, size)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (e *env) total(t *testing.T, q domain.AuditQuery) int64 {
	t.Helper()
	n, err := e.logs.Count(e.ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	return n.Value
}

// ---- Lectura del rastro: paginacion, filtros y orden ----

func TestPaginarNoRepiteNiPierdeFilasNiConFechasIguales(t *testing.T) {
	e := setup(t)
	// Un lote comparte la hora de la transaccion: el orden solo lo desempata el id.
	batch := make([]*domain.AuditLog, 25)
	want := map[uuid.UUID]bool{}
	for i := range batch {
		batch[i] = e.newLog("batch")
		want[batch[i].ID] = true
	}
	if err := e.logs.BulkCreate(e.ctx, batch); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		l := e.newLog("single")
		if err := e.logs.Create(e.ctx, l); err != nil {
			t.Fatal(err)
		}
		want[l.ID] = true
	}

	q := domain.AuditQuery{TenantID: e.tenant}
	if n := e.total(t, q); n != 30 {
		t.Fatalf("total %d", n)
	}
	seen := map[uuid.UUID]bool{}
	var pages [][]*domain.AuditLog
	for page := 1; ; page++ {
		got := e.list(t, q, page, 7)
		if len(got) == 0 {
			break
		}
		pages = append(pages, got)
		for _, l := range got {
			if seen[l.ID] {
				t.Fatalf("la fila %v sale en dos paginas", l.ID)
			}
			seen[l.ID] = true
		}
	}
	if len(pages) != 5 || len(pages[4]) != 2 || len(seen) != 30 {
		t.Fatalf("paginas %d, filas %d", len(pages), len(seen))
	}
	for id := range want {
		if !seen[id] {
			t.Fatalf("falta %v", id)
		}
	}

	var prev time.Time
	again := e.list(t, q, 1, 30)
	for i, l := range again {
		if i > 0 && l.CreatedAt.After(prev) {
			t.Fatal("no esta ordenado de mas reciente a mas antiguo")
		}
		prev = l.CreatedAt
	}
	for i, l := range e.list(t, q, 1, 30) {
		if l.ID != again[i].ID {
			t.Fatal("dos lecturas seguidas dan otro orden")
		}
	}
	if got := e.list(t, q, 6, 7); len(got) != 0 {
		t.Fatalf("una pagina pasada del final devolvio %d filas", len(got))
	}
}

func TestLosFiltrosDelRastroSeCombinan(t *testing.T) {
	e := setup(t)
	alice, bob := uuid.New(), uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	mk := func(user uuid.UUID, action, module, resource, severity, ip string, at time.Time) uuid.UUID {
		id := uuid.New()
		e.tamper(t, `INSERT INTO audit.audit_logs (id,tenant_id,user_id,action,module,resource,ip_address,severity,created_at,seq)
		             VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULL)`, id, e.tenant, user, action, module, resource, ip, severity, at)
		return id
	}
	a1 := mk(alice, "invoice.created", "billing", "invoices", "info", "203.0.113.1", now.Add(-72*time.Hour))
	a2 := mk(alice, "invoice.created", "billing", "invoices", "warning", "203.0.113.2", now.Add(-48*time.Hour))
	a3 := mk(alice, "user.updated", "identity", "users", "info", "203.0.113.1", now.Add(-24*time.Hour))
	b1 := mk(bob, "invoice.deleted", "billing", "invoices", "critical", "198.51.100.5", now.Add(-12*time.Hour))
	b2 := mk(bob, "user.updated", "identity", "users", "info", "198.51.100.5", now)

	str := func(s string) *string { return &s }
	casos := []struct {
		name string
		q    domain.AuditQuery
		want []uuid.UUID
	}{
		{"sin filtro", domain.AuditQuery{}, []uuid.UUID{a1, a2, a3, b1, b2}},
		{"usuario", domain.AuditQuery{UserID: &alice}, []uuid.UUID{a1, a2, a3}},
		{"modulo", domain.AuditQuery{Module: str("billing")}, []uuid.UUID{a1, a2, b1}},
		{"recurso", domain.AuditQuery{Resource: str("users")}, []uuid.UUID{a3, b2}},
		{"accion", domain.AuditQuery{Action: str("invoice.created")}, []uuid.UUID{a1, a2}},
		{"severidad", domain.AuditQuery{Severity: str("critical")}, []uuid.UUID{b1}},
		{"ip", domain.AuditQuery{IPAddress: str("198.51.100.5")}, []uuid.UUID{b1, b2}},
		{"desde inclusivo", domain.AuditQuery{DateFrom: ptr(now.Add(-24 * time.Hour))}, []uuid.UUID{a3, b1, b2}},
		{"hasta inclusivo", domain.AuditQuery{DateTo: ptr(now.Add(-48 * time.Hour))}, []uuid.UUID{a1, a2}},
		{"rango", domain.AuditQuery{DateFrom: ptr(now.Add(-50 * time.Hour)), DateTo: ptr(now.Add(-20 * time.Hour))}, []uuid.UUID{a2, a3}},
		{"todos a la vez", domain.AuditQuery{UserID: &alice, Module: str("billing"), Resource: str("invoices"),
			Action: str("invoice.created"), Severity: str("info"), IPAddress: str("203.0.113.1"),
			DateFrom: ptr(now.Add(-100 * time.Hour)), DateTo: ptr(now)}, []uuid.UUID{a1}},
		{"sin coincidencias", domain.AuditQuery{Module: str("billing"), Severity: str("info"), UserID: &bob}, nil},
		{"valor hostil", domain.AuditQuery{Module: str("billing' OR '1'='1")}, nil},
	}
	for _, c := range casos {
		t.Run(c.name, func(t *testing.T) {
			c.q.TenantID = e.tenant
			got := e.list(t, c.q, 1, 100)
			if len(got) != len(c.want) || e.total(t, c.q) != int64(len(c.want)) {
				t.Fatalf("filas %d, total %d; se esperaban %d", len(got), e.total(t, c.q), len(c.want))
			}
			have := idSet(got)
			for _, id := range c.want {
				if !have[id] {
					t.Fatalf("falta %v", id)
				}
			}
		})
	}
}

func TestLaFilaSeLeeTalComoSeEscribio(t *testing.T) {
	e := setup(t)
	l := e.fullLog("roundtrip")
	if err := e.logs.Create(e.ctx, l); err != nil {
		t.Fatal(err)
	}
	got, err := e.logs.GetByID(e.ctx, l.ID, e.tenant)
	if err != nil || got == nil {
		t.Fatalf("%v %v", got, err)
	}
	if got.ID != l.ID || got.TenantID != e.tenant || got.UserID != e.user || *got.SessionID != *l.SessionID ||
		got.Action != l.Action || got.Module != l.Module || got.Resource != l.Resource || *got.ResourceID != *l.ResourceID ||
		got.IPAddress != l.IPAddress || *got.UserAgent != *l.UserAgent || *got.RequestID != *l.RequestID ||
		got.Severity != "info" || got.CreatedAt.IsZero() {
		t.Fatalf("%+v", got)
	}
	if *got.Before != `{"plan": "basic", "seats": 3}` || *got.After != `{"plan": "pro", "seats": 3}` {
		t.Fatalf("jsonb: %s %s", *got.Before, *got.After)
	}
}

func TestUnIdAusenteDevuelveNilSinError(t *testing.T) {
	e := setup(t)
	got, err := e.logs.GetByID(e.ctx, uuid.New(), e.tenant)
	if err != nil || got != nil {
		t.Fatalf("%v %v", got, err)
	}
}

// ---- Aislamiento entre empresas ----

func TestUnaEmpresaNoLeeElRastroDeOtra(t *testing.T) {
	a := setup(t)
	b := a.otherTenant()
	la, lb := a.fullLog("a.only"), b.fullLog("b.only")
	if err := a.logs.Create(a.ctx, la); err != nil {
		t.Fatal(err)
	}
	if err := b.logs.Create(b.ctx, lb); err != nil {
		t.Fatal(err)
	}

	if got, err := b.logs.GetByID(b.ctx, la.ID, b.tenant); err != nil || got != nil {
		t.Fatalf("B lee un apunte de A por id: %v %v", got, err)
	}
	if got, err := a.logs.GetByID(a.ctx, la.ID, a.tenant); err != nil || got == nil {
		t.Fatalf("A no lee el suyo: %v %v", got, err)
	}
	if got := a.list(t, domain.AuditQuery{TenantID: a.tenant}, 1, 50); len(got) != 1 || got[0].ID != la.ID {
		t.Fatalf("A ve %d filas", len(got))
	}
	if got := b.list(t, domain.AuditQuery{TenantID: b.tenant}, 1, 50); len(got) != 1 || got[0].ID != lb.ID {
		t.Fatalf("B ve %d filas", len(got))
	}
	// Ni siquiera pidiendo el usuario o la accion de la otra empresa.
	if n := b.total(t, domain.AuditQuery{TenantID: b.tenant, UserID: &a.user}); n != 0 {
		t.Fatalf("B cuenta %d filas del usuario de A", n)
	}
	if n := b.total(t, domain.AuditQuery{TenantID: b.tenant, Action: ptr("a.only")}); n != 0 {
		t.Fatalf("B cuenta %d filas con la accion de A", n)
	}

	from, to := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	sum, err := b.summary.GetByModule(b.ctx, b.tenant, from, to)
	if err != nil || len(sum) != 1 || sum[0].TotalActions != 1 {
		t.Fatalf("resumen de B: %+v %v", sum, err)
	}
	if n, err := b.summary.GetUserActivity(b.ctx, b.tenant, a.user, from, to); err != nil || n != 0 {
		t.Fatalf("actividad del usuario de A vista desde B: %d %v", n, err)
	}
}

func TestLasConsultasDelDetectorNoCruzanEmpresas(t *testing.T) {
	a := setup(t)
	b := a.otherTenant()
	ua := "Mozilla/5.0 A"
	a.legacy(t, a.tenant, a.user, "user.logged_in", "203.0.113.20", &ua, time.Now().Add(-time.Minute))

	if ok, err := b.logs.HasUserActionFromIP(b.ctx, b.tenant, a.user, "user.logged_in", "203.0.113.20", uuid.Nil); err != nil || ok {
		t.Fatalf("HasUserActionFromIP cruza empresas: %v %v", ok, err)
	}
	if got, err := b.logs.ListUserActionAgents(b.ctx, b.tenant, a.user, "user.logged_in", uuid.Nil, 10); err != nil || len(got) != 0 {
		t.Fatalf("ListUserActionAgents cruza empresas: %v %v", got, err)
	}
	if n, err := b.logs.CountRecentByActionIP(b.ctx, b.tenant, "user.logged_in", "203.0.113.20", time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("CountRecentByActionIP cruza empresas: %d %v", n, err)
	}
	if ip, err := b.logs.RecentLoginOtherIP(b.ctx, b.tenant, a.user, "198.51.100.1", time.Now().Add(-time.Hour), uuid.Nil); err != nil || ip != "" {
		t.Fatalf("RecentLoginOtherIP cruza empresas: %q %v", ip, err)
	}

	if ok, _ := a.logs.HasUserActionFromIP(a.ctx, a.tenant, a.user, "user.logged_in", "203.0.113.20", uuid.Nil); !ok {
		t.Fatal("la propia empresa no ve su historial")
	}
}

func TestLasConsultasDelDetectorExcluyenElApunteActual(t *testing.T) {
	e := setup(t)
	ua1, ua2 := "Firefox", "Chrome"
	first := e.legacy(t, e.tenant, e.user, "user.logged_in", "203.0.113.1", &ua1, time.Now().Add(-2*time.Minute))
	current := e.legacy(t, e.tenant, e.user, "user.logged_in", "203.0.113.9", &ua2, time.Now().Add(-time.Minute))
	e.legacy(t, e.tenant, e.user, "user.logged_in", "203.0.113.9", nil, time.Now().Add(-30*time.Second))
	e.legacy(t, e.tenant, e.user, "user.logged_in", "203.0.113.9", ptr(""), time.Now().Add(-20*time.Second))
	e.legacy(t, e.tenant, uuid.New(), "user.logged_in", "203.0.113.9", ptr("OtroUsuario"), time.Now())

	if ok, _ := e.logs.HasUserActionFromIP(e.ctx, e.tenant, e.user, "user.logged_in", "203.0.113.9", uuid.Nil); !ok {
		t.Fatal("la IP aparece en el historial")
	}
	if ok, _ := e.logs.HasUserActionFromIP(e.ctx, e.tenant, e.user, "user.logged_in", "203.0.113.1", first); ok {
		t.Fatal("el apunte excluido se conto como historial")
	}
	if ok, _ := e.logs.HasUserActionFromIP(e.ctx, e.tenant, e.user, "otra.accion", "203.0.113.9", uuid.Nil); ok {
		t.Fatal("otra accion no es historial de esta")
	}

	agents, err := e.logs.ListUserActionAgents(e.ctx, e.tenant, e.user, "user.logged_in", current, 10)
	if err != nil || len(agents) != 1 || agents[0] != ua1 {
		t.Fatalf("agentes %v %v: sin el actual, sin nulos ni vacios, sin los de otro usuario", agents, err)
	}
	if agents, _ := e.logs.ListUserActionAgents(e.ctx, e.tenant, e.user, "user.logged_in", uuid.Nil, 1); len(agents) != 1 {
		t.Fatalf("el limite no se respeta: %v", agents)
	}

	since := time.Now().Add(-90 * time.Second)
	if n, _ := e.logs.CountRecentByActionIP(e.ctx, e.tenant, "user.logged_in", "203.0.113.9", since); n != 4 {
		t.Fatalf("intentos recientes desde la IP: %d, se esperaban 4 (de todos los usuarios)", n)
	}
	if n, _ := e.logs.CountRecentByActionIP(e.ctx, e.tenant, "user.logged_in", "203.0.113.9", time.Now().Add(-25*time.Second)); n != 2 {
		t.Fatalf("la ventana no acota: %d", n)
	}

	ip, err := e.logs.RecentLoginOtherIP(e.ctx, e.tenant, e.user, "203.0.113.9", time.Now().Add(-time.Hour), current)
	if err != nil || ip != "203.0.113.1" {
		t.Fatalf("otra IP reciente: %q %v", ip, err)
	}
	if ip, _ := e.logs.RecentLoginOtherIP(e.ctx, e.tenant, e.user, "203.0.113.1", time.Now().Add(-time.Hour), uuid.Nil); ip == "" || ip == "203.0.113.1" {
		t.Fatalf("debe devolver una IP distinta de la actual: %q", ip)
	}
	if ip, _ := e.logs.RecentLoginOtherIP(e.ctx, e.tenant, e.user, "203.0.113.9", time.Now().Add(-time.Second), uuid.Nil); ip != "" {
		t.Fatalf("fuera de la ventana no hay senal: %q", ip)
	}
}

func TestElResumenAgrupaPorModuloDentroDelRango(t *testing.T) {
	e := setup(t)
	other := uuid.New()
	now := time.Now().UTC()
	e.legacy(t, e.tenant, e.user, "a", "203.0.113.1", nil, now.Add(-2*time.Hour))
	e.legacy(t, e.tenant, other, "b", "203.0.113.1", nil, now.Add(-time.Hour))
	e.legacy(t, e.tenant, e.user, "c", "203.0.113.1", nil, now.Add(-30*24*time.Hour))
	e.tamper(t, `UPDATE audit.audit_logs SET module = 'billing' WHERE action = 'b'`)

	got, err := e.summary.GetByModule(e.ctx, e.tenant, now.Add(-3*time.Hour), now)
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if got[0].Module != "billing" || got[0].TotalActions != 1 || got[1].Module != "identity" || got[1].TotalActions != 1 {
		t.Fatalf("orden por modulo o cuentas: %+v %+v", got[0], got[1])
	}
	if !got[0].LastActivity.Before(now) {
		t.Fatalf("ultima actividad %v", got[0].LastActivity)
	}

	e.legacy(t, e.tenant, other, "d", "203.0.113.1", nil, now.Add(-90*time.Minute))
	got, _ = e.summary.GetByModule(e.ctx, e.tenant, now.Add(-3*time.Hour), now)
	for _, s := range got {
		if s.Module == "identity" && (s.TotalActions != 2 || s.UniqueUsers != 2) {
			t.Fatalf("usuarios distintos: %+v", s)
		}
	}
	if n, _ := e.summary.GetUserActivity(e.ctx, e.tenant, e.user, now.Add(-3*time.Hour), now); n != 1 {
		t.Fatalf("actividad del usuario en el rango: %d", n)
	}
	if n, _ := e.summary.GetUserActivity(e.ctx, e.tenant, e.user, now.Add(-31*24*time.Hour), now); n != 2 {
		t.Fatalf("actividad del usuario ampliando el rango: %d", n)
	}
	if got, err := e.summary.GetByModule(e.ctx, e.tenant, now.Add(time.Hour), now.Add(2*time.Hour)); err != nil || len(got) != 0 {
		t.Fatalf("rango futuro: %v %v", got, err)
	}
}

// ---- Eventos de seguridad ----

func (e *env) newEvent(eventType, risk, ip string) *domain.SecurityEvent {
	return &domain.SecurityEvent{
		ID: uuid.New(), TenantID: e.tenant, UserID: &e.user, EventType: eventType,
		IPAddress: ip, UserAgent: ptr("Mozilla/5.0"), Detail: "detalle de " + eventType, RiskLevel: risk,
	}
}

func TestUnEventoDeSeguridadSeLeeTalComoSeEscribio(t *testing.T) {
	e := setup(t)
	for _, ip := range []string{"203.0.113.9", "2001:db8::1"} {
		evt := e.newEvent("failed_login", "high", ip)
		if err := e.security.Create(e.ctx, evt); err != nil {
			t.Fatalf("%s: %v", ip, err)
		}
		got, err := e.security.GetByID(e.ctx, evt.ID, e.tenant)
		if err != nil || got == nil {
			t.Fatalf("%s: %v %v", ip, got, err)
		}
		if got.IPAddress != ip || got.EventType != "failed_login" || got.RiskLevel != "high" || got.Acknowledged ||
			got.AcknowledgedBy != nil || got.Detail != evt.Detail || *got.UserID != e.user || got.CreatedAt.IsZero() {
			t.Fatalf("%s: %+v", ip, got)
		}
	}
	if got, err := e.security.GetByID(e.ctx, uuid.New(), e.tenant); err != nil || got != nil {
		t.Fatalf("ausente: %v %v", got, err)
	}
}

func TestLaBaseRechazaEventosMalFormados(t *testing.T) {
	e := setup(t)
	if err := e.security.Create(e.ctx, e.newEvent("failed_login", "extreme", "203.0.113.9")); err == nil {
		t.Fatal("un nivel de riesgo fuera del catalogo debe rechazarse")
	}
	if err := e.security.Create(e.ctx, e.newEvent("failed_login", "low", "no-es-ip")); err == nil {
		t.Fatal("una IP invalida debe rechazarse")
	}
	if n := e.count(t, `SELECT count(*) FROM audit.security_events`); n != 0 {
		t.Fatalf("%d eventos rechazados quedaron escritos", n)
	}
}

func TestLosEventosDeSeguridadSeFiltranYPaginan(t *testing.T) {
	e := setup(t)
	now := time.Now().UTC()
	mk := func(eventType, risk, ip string, acked bool, at time.Time) uuid.UUID {
		id := uuid.New()
		e.tamper(t, `INSERT INTO audit.security_events (id,tenant_id,user_id,event_type,ip_address,detail,risk_level,acknowledged,created_at)
		             VALUES ($1,$2,$3,$4,$5,'d',$6,$7,$8)`, id, e.tenant, e.user, eventType, ip, risk, acked, at)
		return id
	}
	s1 := mk("failed_login", "high", "203.0.113.1", false, now.Add(-4*time.Hour))
	s2 := mk("failed_login", "low", "203.0.113.2", true, now.Add(-3*time.Hour))
	s3 := mk("data_export", "critical", "203.0.113.3", false, now.Add(-2*time.Hour))
	s4 := mk("permission_change", "high", "203.0.113.4", true, now.Add(-1*time.Hour))
	str := func(s string) *string { return &s }

	casos := []struct {
		name string
		f    ports.SecurityFilters
		want []uuid.UUID
	}{
		{"todos, mas reciente primero", ports.SecurityFilters{}, []uuid.UUID{s4, s3, s2, s1}},
		{"tipo", ports.SecurityFilters{EventType: str("failed_login")}, []uuid.UUID{s2, s1}},
		{"riesgo", ports.SecurityFilters{RiskLevel: str("high")}, []uuid.UUID{s4, s1}},
		{"pendientes", ports.SecurityFilters{Acknowledged: ptr(false)}, []uuid.UUID{s3, s1}},
		{"reconocidos", ports.SecurityFilters{Acknowledged: ptr(true)}, []uuid.UUID{s4, s2}},
		{"rango", ports.SecurityFilters{DateFrom: ptr(now.Add(-210 * time.Minute)), DateTo: ptr(now.Add(-90 * time.Minute))}, []uuid.UUID{s3, s2}},
		{"combinados", ports.SecurityFilters{EventType: str("failed_login"), RiskLevel: str("high"), Acknowledged: ptr(false)}, []uuid.UUID{s1}},
		{"sin coincidencias", ports.SecurityFilters{EventType: str("data_export"), Acknowledged: ptr(true)}, nil},
	}
	for _, c := range casos {
		t.Run(c.name, func(t *testing.T) {
			got, total, err := e.security.List(e.ctx, e.tenant, c.f, 1, 50)
			if err != nil || total != int64(len(c.want)) || len(got) != len(c.want) {
				t.Fatalf("filas %d total %d err %v; se esperaban %d", len(got), total, err, len(c.want))
			}
			for i, evt := range got {
				if evt.ID != c.want[i] {
					t.Fatalf("posicion %d: %v, se esperaba %v", i, evt.ID, c.want[i])
				}
			}
		})
	}

	page1, total, _ := e.security.List(e.ctx, e.tenant, ports.SecurityFilters{}, 1, 3)
	page2, _, _ := e.security.List(e.ctx, e.tenant, ports.SecurityFilters{}, 2, 3)
	if total != 4 || len(page1) != 3 || len(page2) != 1 || page2[0].ID != s1 {
		t.Fatalf("paginacion: total %d, %d + %d", total, len(page1), len(page2))
	}

	pending, err := e.security.GetUnacknowledged(e.ctx, e.tenant)
	if err != nil || len(pending) != 2 || pending[0].ID != s3 || pending[1].ID != s1 {
		t.Fatalf("pendientes: %+v %v", pending, err)
	}
}

func TestReconocerUnEventoConservaAQuienLoHizoPrimero(t *testing.T) {
	e := setup(t)
	evt := e.newEvent("failed_login", "high", "203.0.113.9")
	if err := e.security.Create(e.ctx, evt); err != nil {
		t.Fatal(err)
	}
	first, second := uuid.New(), uuid.New()
	if err := e.security.Acknowledge(e.ctx, evt.ID, first); err != nil {
		t.Fatal(err)
	}
	got, _ := e.security.GetByID(e.ctx, evt.ID, e.tenant)
	if !got.Acknowledged || got.AcknowledgedBy == nil || *got.AcknowledgedBy != first || got.AcknowledgedAt == nil {
		t.Fatalf("%+v", got)
	}
	at := *got.AcknowledgedAt

	time.Sleep(20 * time.Millisecond)
	if err := e.security.Acknowledge(e.ctx, evt.ID, second); err != nil {
		t.Fatal(err)
	}
	got, _ = e.security.GetByID(e.ctx, evt.ID, e.tenant)
	if *got.AcknowledgedBy != first || !got.AcknowledgedAt.Equal(at) {
		t.Fatalf("un segundo reconocimiento reescribio quien y cuando: %v %v", *got.AcknowledgedBy, got.AcknowledgedAt)
	}
	if pending, _ := e.security.GetUnacknowledged(e.ctx, e.tenant); len(pending) != 0 {
		t.Fatalf("pendientes: %d", len(pending))
	}
}

func TestLaAlertaRecienteSeDeduplicaPorTipoIPYVentana(t *testing.T) {
	e := setup(t)
	evt := e.newEvent("failed_login", "high", "203.0.113.9")
	if err := e.security.Create(e.ctx, evt); err != nil {
		t.Fatal(err)
	}
	other := e.otherTenant()
	casos := []struct {
		name  string
		env   *env
		typ   string
		ip    string
		since time.Time
		want  bool
	}{
		{"misma alerta", e, "failed_login", "203.0.113.9", time.Now().Add(-time.Hour), true},
		{"otra IP", e, "failed_login", "203.0.113.10", time.Now().Add(-time.Hour), false},
		{"otro tipo", e, "data_export", "203.0.113.9", time.Now().Add(-time.Hour), false},
		{"fuera de la ventana", e, "failed_login", "203.0.113.9", time.Now().Add(time.Minute), false},
		{"otra empresa", other, "failed_login", "203.0.113.9", time.Now().Add(-time.Hour), false},
	}
	for _, c := range casos {
		got, err := c.env.security.HasRecentEvent(c.env.ctx, c.env.tenant, c.typ, c.ip, c.since)
		if err != nil || got != c.want {
			t.Errorf("%s: %v %v, se esperaba %v", c.name, got, err, c.want)
		}
	}
}

func TestUnaEmpresaNoVeLosEventosDeSeguridadDeOtra(t *testing.T) {
	a := setup(t)
	b := a.otherTenant()
	ea := a.newEvent("failed_login", "high", "203.0.113.9")
	if err := a.security.Create(a.ctx, ea); err != nil {
		t.Fatal(err)
	}
	if got, err := b.security.GetByID(b.ctx, ea.ID, b.tenant); err != nil || got != nil {
		t.Fatalf("GetByID cruza empresas: %v %v", got, err)
	}
	if got, total, err := b.security.List(b.ctx, b.tenant, ports.SecurityFilters{}, 1, 50); err != nil || total != 0 || len(got) != 0 {
		t.Fatalf("List cruza empresas: %d %v", total, err)
	}
	if got, err := b.security.GetUnacknowledged(b.ctx, b.tenant); err != nil || len(got) != 0 {
		t.Fatalf("GetUnacknowledged cruza empresas: %v %v", got, err)
	}

	uc := app.NewAuditUseCase(app.AuditDeps{Logs: b.logs, Security: b.security, Changes: b.changes, Summary: b.summary, Logger: zap.NewNop()})
	err := uc.AcknowledgeSecurityEvent(b.ctx, ea.ID, b.tenant, b.user)
	if !errors.Is(err, domain.ErrSecurityEventNotFound) {
		t.Fatalf("B reconocio un evento de A: %v", err)
	}
	got, _ := a.security.GetByID(a.ctx, ea.ID, a.tenant)
	if got.Acknowledged {
		t.Fatal("el evento de A quedo reconocido desde B")
	}
}

// ---- Cambios campo a campo ----

func TestLosCambiosDeUnApunteSeGuardanYSeAislanPorEmpresa(t *testing.T) {
	a := setup(t)
	b := a.otherTenant()
	l := a.fullLog("changed")
	if err := a.logs.Create(a.ctx, l); err != nil {
		t.Fatal(err)
	}
	uc := app.NewAuditUseCase(app.AuditDeps{Logs: a.logs, Security: a.security, Changes: a.changes, Summary: a.summary, Logger: zap.NewNop()})
	if err := uc.CompareChanges(a.ctx, a.tenant, l.ID, `{"plan":"basic","seats":3}`, `{"plan":"pro","seats":3,"note":"x"}`); err != nil {
		t.Fatal(err)
	}
	got, err := a.changes.GetByLogID(a.ctx, a.tenant, l.ID)
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := b.changes.GetByLogID(b.ctx, b.tenant, l.ID); err != nil || len(got) != 0 {
		t.Fatalf("B lee los cambios de A: %v %v", got, err)
	}
}

func TestLosCambiosSeEscribenTodosONinguno(t *testing.T) {
	e := setup(t)
	l := e.fullLog("changed")
	if err := e.logs.Create(e.ctx, l); err != nil {
		t.Fatal(err)
	}
	good := &domain.DataChangeRecord{ID: uuid.New(), TenantID: e.tenant, AuditLogID: l.ID, FieldName: "f", NewValue: ptr("1")}
	orphan := &domain.DataChangeRecord{ID: uuid.New(), TenantID: e.tenant, AuditLogID: uuid.New(), FieldName: "g"}
	if err := e.changes.CreateBatch(e.ctx, []*domain.DataChangeRecord{good, orphan}); err == nil {
		t.Fatal("un cambio de un apunte que no existe debe rechazarse")
	}
	if n := e.count(t, `SELECT count(*) FROM audit.data_change_records`); n != 0 {
		t.Fatalf("el lote fallido dejo %d filas", n)
	}
}

func TestRepetirUnLoteDeCambiosNoLosDuplica(t *testing.T) {
	e := setup(t)
	l := e.fullLog("changed")
	if err := e.logs.Create(e.ctx, l); err != nil {
		t.Fatal(err)
	}
	rec := &domain.DataChangeRecord{ID: uuid.New(), TenantID: e.tenant, AuditLogID: l.ID, FieldName: "f", OldValue: ptr("a"), NewValue: ptr("b")}
	for i := 0; i < 2; i++ {
		if err := e.changes.CreateBatch(e.ctx, []*domain.DataChangeRecord{rec}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.changes.GetByLogID(e.ctx, e.tenant, l.ID)
	if err != nil || len(got) != 1 || *got[0].OldValue != "a" || *got[0].NewValue != "b" || got[0].FieldName != "f" {
		t.Fatalf("%+v %v", got, err)
	}
}
