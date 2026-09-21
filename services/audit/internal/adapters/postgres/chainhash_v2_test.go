package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/google/uuid"
)

const (
	testKeyA = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	testKeyB = "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f"
	// Identificador de testKeyA, calculado fuera de Go (HMAC-SHA256 de la etiqueta fija).
	testKeyAID = "16157e30eddc89b3"
)

func testRing(t *testing.T, active, old string) *crypto.MACKeyRing {
	t.Helper()
	t.Setenv("AUDIT_TEST_HASH_KEY", active)
	t.Setenv("AUDIT_TEST_HASH_KEYS_OLD", old)
	kr, err := crypto.LoadMACKeyRing("AUDIT_TEST_HASH_KEY", "AUDIT_TEST_HASH_KEYS_OLD")
	if err != nil || kr == nil {
		t.Fatalf("anillo: %v %v", kr, err)
	}
	return kr
}

func fullStored() domain.AuditLog {
	l := hashLog
	l.UserAgent = ptr("Mozilla/5.0 test")
	l.Before, l.After, l.Changes = ptr(hashBefore), ptr(hashAfter), ptr(hashChanges)
	l.CreatedAt = hashTime
	return l
}

func v2Hash(t *testing.T, ring *crypto.MACKeyRing, seq int64, prev string, l *domain.AuditLog) string {
	t.Helper()
	got, err := signCanonical(ring, ring.ActiveID(), auditLogCanonicalV2(ring.ActiveID(), seq, prev, l))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// El formato de la version 2 es un contrato con las filas ya firmadas: cambiarlo las lee como
// manipuladas. Los vectores los calculo un HMAC-SHA256 aparte, fuera de Go, sobre la
// serializacion documentada en docs/adr/0006.
func TestElFormatoDeLaVersion2NoCambia(t *testing.T) {
	ring := testRing(t, testKeyA, "")
	if ring.ActiveID() != testKeyAID {
		t.Fatalf("id de llave %s, se esperaba %s", ring.ActiveID(), testKeyAID)
	}
	l := fullStored()
	if got, want := v2Hash(t, ring, 42, "prevhash0", &l), "03a4131473dc83eac2ba07b04f6bb972dde5d3698f601d5137f5bea5e53ba800"; got != want {
		t.Fatalf("audit_logs %s, se esperaba %s", got, want)
	}

	sparse := hashLog
	sparse.SessionID, sparse.ResourceID, sparse.RequestID = nil, nil, nil
	sparse.Severity, sparse.CreatedAt = "info", hashTime
	if got, want := v2Hash(t, ring, 1, "", &sparse), "6dc5c3950c9a59186b9d3c0d6ebd8cf13223769c4ee46cd41a335b1845fc6c3d"; got != want {
		t.Fatalf("audit_logs sin opcionales %s, se esperaba %s", got, want)
	}

	e := securityEventRecord{
		ID: hashLog.ID, TenantID: hashLog.TenantID, UserID: ptr(hashLog.UserID), EventType: "failed_login",
		IP: ptr("203.0.113.7"), UserAgent: ptr("Mozilla/5.0 test"), Detail: ptr("intentos=5"), RiskLevel: "high", CreatedAt: hashTime,
	}
	got, err := signCanonical(ring, ring.ActiveID(), securityEventCanonicalV2(ring.ActiveID(), 7, "prevhash0", &e))
	if err != nil {
		t.Fatal(err)
	}
	if want := "e29251729b4c8f766f43b458c58f7690614b24327117ef8c1fda779017049639"; got != want {
		t.Fatalf("security_events %s, se esperaba %s", got, want)
	}
}

// Es el caso demostrado contra la version 1: mover el separador entre dos campos contiguos da el
// mismo hash. Con longitud por campo, la serializacion de la version 2 es distinta.
func TestLaColisionDeLaVersion1NoExisteEnLaVersion2(t *testing.T) {
	ring := testRing(t, testKeyA, "")
	pares := map[string]func(a, b *domain.AuditLog){
		"action y module": func(a, b *domain.AuditLog) {
			a.Action, a.Module = "user.updated|x", "identity"
			b.Action, b.Module = "user.updated", "x|identity"
		},
		"module y resource": func(a, b *domain.AuditLog) {
			a.Module, a.Resource = "identity|users", "u"
			b.Module, b.Resource = "identity", "users|u"
		},
		"resource y resource_id": func(a, b *domain.AuditLog) {
			a.Resource, a.ResourceID = "users", ptr("u-1|x")
			b.Resource, b.ResourceID = "users|u-1", ptr("x")
		},
	}
	for name, fn := range pares {
		t.Run(name, func(t *testing.T) {
			a, b := fullStored(), fullStored()
			fn(&a, &b)
			if chainHash("p", &a, hashBefore, hashAfter, hashChanges, hashTime) != chainHash("p", &b, hashBefore, hashAfter, hashChanges, hashTime) {
				t.Fatal("la colision de la version 1 dejo de reproducirse: esta prueba ya no demuestra nada")
			}
			if v2Hash(t, ring, 3, "p", &a) == v2Hash(t, ring, 3, "p", &b) {
				t.Fatal("la version 2 repite la colision de la version 1")
			}
			if string(auditLogCanonicalV2(ring.ActiveID(), 3, "p", &a)) == string(auditLogCanonicalV2(ring.ActiveID(), 3, "p", &b)) {
				t.Fatal("la serializacion es ambigua")
			}
		})
	}
}

func TestCadaCampoDeLaVersion2AlteraElHash(t *testing.T) {
	ring := testRing(t, testKeyA, "")
	base := fullStored()
	want := v2Hash(t, ring, 5, "p", &base)
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
		"sin recurso": func(l *domain.AuditLog) { l.ResourceID = nil },
		"ip_address":  func(l *domain.AuditLog) { l.IPAddress = "198.51.100.1" },
		"user_agent":  func(l *domain.AuditLog) { l.UserAgent = ptr("curl/8.0") },
		"sin agente":  func(l *domain.AuditLog) { l.UserAgent = nil },
		"request_id":  func(l *domain.AuditLog) { l.RequestID = ptr("req-10") },
		"before":      func(l *domain.AuditLog) { l.Before = ptr(`{"plan": "x"}`) },
		"sin before":  func(l *domain.AuditLog) { l.Before = nil },
		"after":       func(l *domain.AuditLog) { l.After = ptr(`{"plan": "x"}`) },
		"changes":     func(l *domain.AuditLog) { l.Changes = ptr(`{"plan": "x"}`) },
		"severity":    func(l *domain.AuditLog) { l.Severity = "critical" },
		"created_at":  func(l *domain.AuditLog) { l.CreatedAt = l.CreatedAt.Add(time.Microsecond) },
	}
	for name, fn := range mutate {
		l := fullStored()
		fn(&l)
		if v2Hash(t, ring, 5, "p", &l) == want {
			t.Errorf("cambiar %s no altera el hash", name)
		}
	}
	if v2Hash(t, ring, 6, "p", &base) == want {
		t.Error("cambiar seq no altera el hash: una fila se podria mover de sitio")
	}
	if v2Hash(t, ring, 5, "q", &base) == want {
		t.Error("cambiar prev_hash no altera el hash")
	}
	otherKey := testRing(t, testKeyB, "")
	if v2Hash(t, otherKey, 5, "p", &base) == want {
		t.Error("otra llave da el mismo hash")
	}
}

// NULL y cadena vacia son cosas distintas: en la version 1 se confundian.
func TestLaVersion2DistingueNuloDeVacio(t *testing.T) {
	ring := testRing(t, testKeyA, "")
	for name, fn := range map[string]func(l *domain.AuditLog, v *string){
		"resource_id": func(l *domain.AuditLog, v *string) { l.ResourceID = v },
		"user_agent":  func(l *domain.AuditLog, v *string) { l.UserAgent = v },
		"request_id":  func(l *domain.AuditLog, v *string) { l.RequestID = v },
		"before":      func(l *domain.AuditLog, v *string) { l.Before = v },
		"after":       func(l *domain.AuditLog, v *string) { l.After = v },
		"changes":     func(l *domain.AuditLog, v *string) { l.Changes = v },
	} {
		nulo, vacio := fullStored(), fullStored()
		fn(&nulo, nil)
		fn(&vacio, ptr(""))
		if v2Hash(t, ring, 1, "p", &nulo) == v2Hash(t, ring, 1, "p", &vacio) {
			t.Errorf("%s: NULL y vacio dan el mismo hash", name)
		}
	}
}

func TestLaVersion2NoDependeDeLaZonaHorariaNiDeLaPrecisionSobrante(t *testing.T) {
	ring := testRing(t, testKeyA, "")
	a, b := fullStored(), fullStored()
	b.CreatedAt = a.CreatedAt.In(time.FixedZone("PET", -5*3600))
	if v2Hash(t, ring, 1, "p", &a) != v2Hash(t, ring, 1, "p", &b) {
		t.Fatal("el mismo instante en otra zona da otro hash")
	}
}

func TestUnaFilaDeUnaCadenaNoServeEnLaOtra(t *testing.T) {
	ring := testRing(t, testKeyA, "")
	id := hashLog.ID
	logsBytes := auditLogCanonicalV2(ring.ActiveID(), 1, "", &domain.AuditLog{ID: id})
	eventsBytes := securityEventCanonicalV2(ring.ActiveID(), 1, "", &securityEventRecord{ID: id})
	if !strings.Contains(string(logsBytes), string(domain.ChainAuditLogs)) || !strings.Contains(string(eventsBytes), string(domain.ChainSecurityEvents)) {
		t.Fatal("la serializacion no lleva la cadena a la que pertenece")
	}
}

func TestCadaCampoDeUnEventoDeSeguridadAlteraElHash(t *testing.T) {
	ring := testRing(t, testKeyA, "")
	base := securityEventRecord{
		ID: uuid.New(), TenantID: uuid.New(), UserID: ptr(uuid.New()), EventType: "failed_login",
		IP: ptr("203.0.113.7"), UserAgent: ptr("ua"), Detail: ptr("d"), RiskLevel: "low", CreatedAt: hashTime,
	}
	hash := func(seq int64, prev string, e securityEventRecord) string {
		got, err := signCanonical(ring, ring.ActiveID(), securityEventCanonicalV2(ring.ActiveID(), seq, prev, &e))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	want := hash(1, "p", base)
	mutate := map[string]func(e *securityEventRecord){
		"id":         func(e *securityEventRecord) { e.ID = uuid.New() },
		"tenant_id":  func(e *securityEventRecord) { e.TenantID = uuid.New() },
		"user_id":    func(e *securityEventRecord) { e.UserID = nil },
		"event_type": func(e *securityEventRecord) { e.EventType = "data_export" },
		"ip":         func(e *securityEventRecord) { e.IP = ptr("198.51.100.1") },
		"user_agent": func(e *securityEventRecord) { e.UserAgent = nil },
		"detail":     func(e *securityEventRecord) { e.Detail = ptr("otro") },
		"risk_level": func(e *securityEventRecord) { e.RiskLevel = "critical" },
		"created_at": func(e *securityEventRecord) { e.CreatedAt = e.CreatedAt.Add(time.Microsecond) },
	}
	for name, fn := range mutate {
		e := base
		fn(&e)
		if hash(1, "p", e) == want {
			t.Errorf("cambiar %s no altera el hash", name)
		}
	}
	if hash(2, "p", base) == want || hash(1, "q", base) == want {
		t.Error("seq y prev_hash deben entrar en el hash")
	}
}

// ---- Verificador: causas de rotura ----

func verifierRow(version int, keyID string) storedRow {
	return storedRow{id: uuid.New(), tenantID: hashLog.TenantID, seq: ptr(int64(1)), version: version, keyID: keyID}
}

func TestElVerificadorPideLaLlaveParaUnaFilaDeVersion2(t *testing.T) {
	v := newChainVerifier(domain.ChainAuditLogs, nil, hashLog.TenantID)
	row := verifierRow(hashVersionKeyed, testKeyAID)
	if v.check(row, func() string { return "" }, func(string, int64) []byte { return nil }) {
		t.Fatal("una fila de version 2 sin clave no puede darse por buena")
	}
	if v.res.OK || v.res.Reason != domain.ReasonHashKeyMissing || v.res.BrokenVersion == nil || *v.res.BrokenVersion != 2 {
		t.Fatalf("%+v", v.res)
	}
}

func TestElVerificadorDistingueUnaLlaveRetiradaDeUnaFilaManipulada(t *testing.T) {
	ring := testRing(t, testKeyB, "")
	v := newChainVerifier(domain.ChainAuditLogs, ring, hashLog.TenantID)
	row := verifierRow(hashVersionKeyed, testKeyAID)
	if v.check(row, nil, func(string, int64) []byte { return []byte("x") }) || v.res.Reason != domain.ReasonHashKeyUnknown {
		t.Fatalf("%+v", v.res)
	}

	v = newChainVerifier(domain.ChainAuditLogs, ring, hashLog.TenantID)
	row = verifierRow(hashVersionKeyed, ring.ActiveID())
	row.entry = strings.Repeat("0", 64)
	if v.check(row, nil, func(string, int64) []byte { return []byte("x") }) || v.res.Reason != domain.ReasonChainBroken {
		t.Fatalf("%+v", v.res)
	}
}

func TestElVerificadorNoAceptaQueLaVersionRetroceda(t *testing.T) {
	ring := testRing(t, testKeyA, "")
	v := newChainVerifier(domain.ChainAuditLogs, ring, hashLog.TenantID)
	first := verifierRow(hashVersionKeyed, ring.ActiveID())
	canon := func(k string, seq int64) []byte { return []byte("fila-1") }
	first.entry, _ = signCanonical(ring, ring.ActiveID(), canon(ring.ActiveID(), 1))
	if !v.check(first, nil, canon) {
		t.Fatalf("%+v", v.res)
	}
	second := verifierRow(hashVersionUnkeyed, "")
	second.prev = first.entry
	second.entry = "cualquiera"
	if v.check(second, func() string { return "cualquiera" }, nil) || v.res.Reason != domain.ReasonHashVersionRegression {
		t.Fatalf("una fila sin clave tras una firmada debe ser una regresion: %+v", v.res)
	}
}

func TestElVerificadorRechazaVersionesQueNoConoce(t *testing.T) {
	for _, version := range []int{0, 3, -1} {
		v := newChainVerifier(domain.ChainAuditLogs, nil, hashLog.TenantID)
		if v.check(verifierRow(version, ""), func() string { return "" }, nil) || v.res.Reason != domain.ReasonHashVersionUnsupported {
			t.Errorf("version %d: %+v", version, v.res)
		}
	}
	v := newChainVerifier(domain.ChainSecurityEvents, nil, hashLog.TenantID)
	if v.check(verifierRow(hashVersionUnkeyed, ""), nil, nil) || v.res.Reason != domain.ReasonHashVersionUnsupported {
		t.Errorf("la cadena de eventos no tiene version 1: %+v", v.res)
	}
}

func TestElVerificadorNoNombraFilasDeOtraEmpresa(t *testing.T) {
	v := newChainVerifier(domain.ChainAuditLogs, nil, uuid.New())
	row := verifierRow(hashVersionUnkeyed, "")
	row.entry = "no-cuadra"
	if v.check(row, func() string { return "otro" }, nil) {
		t.Fatal("debia romperse")
	}
	if v.res.OK || v.res.BrokenID != nil || v.res.BrokenSeq != nil {
		t.Fatalf("la respuesta identifica una fila de otra empresa: %+v", v.res)
	}
}

func TestUnaFilaDeVersion2SinPosicionNoSePuedeUbicar(t *testing.T) {
	ring := testRing(t, testKeyA, "")
	v := newChainVerifier(domain.ChainAuditLogs, ring, hashLog.TenantID)
	row := verifierRow(hashVersionKeyed, ring.ActiveID())
	row.seq = nil
	if v.check(row, nil, func(string, int64) []byte { return nil }) || v.res.Reason != domain.ReasonChainBroken {
		t.Fatalf("%+v", v.res)
	}
}
