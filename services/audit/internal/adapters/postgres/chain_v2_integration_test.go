//go:build integration

// Cadena de hash version 2 (docs/adr/0006) contra un Postgres real: cadena mixta, manipulacion,
// recalculo sin la clave, rotacion de llaves, anclas, cadena de eventos de seguridad y
// concurrencia. Corre con el mismo AUDIT_TEST_DSN y el mismo rol de servicio que integration_test.go.
package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/pkg/db"
	outboxadapter "github.com/alonsosss/corforce-email/services/audit/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/audit/internal/app"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// useKeys cambia las claves con las que escribe y verifica el servicio, como un reinicio con otra
// configuracion.
func (e *env) useKeys(ring *crypto.MACKeyRing) {
	cp := &db.ContextPool{}
	e.logs = NewAuditLogRepo(cp, ring)
	e.security = NewSecurityEventRepo(cp, ring)
}

func (e *env) usecase() *app.AuditUseCase {
	cp := &db.ContextPool{}
	return app.NewAuditUseCase(app.AuditDeps{
		Logs: e.logs, Security: e.security, Changes: e.changes, Summary: e.summary, Logger: zap.NewNop(),
		Anchors: e.anchors, AnchorEvents: outboxadapter.NewPublisher(cp), Tx: cp,
	})
}

func (e *env) verifyAll(t *testing.T) *domain.ChainIntegrity {
	t.Helper()
	res, err := e.usecase().VerifyChainIntegrity(e.ctx, e.tenant)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (e *env) anchorAll(t *testing.T) []app.AnchorResult {
	t.Helper()
	res, err := e.usecase().AnchorChains(e.ctx, e.tenant)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (e *env) seedEvents(t *testing.T, n int) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		evt := e.newEvent("failed_login", "high", "203.0.113.9")
		if err := e.security.Create(e.ctx, evt); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, evt.ID)
	}
	return ids
}

// storedLog es una fila de audit_logs tal como la lee quien tiene acceso a la base.
type storedLog struct {
	seq   int64
	entry string
	log   domain.AuditLog
}

func (e *env) storedLogs(t *testing.T) []storedLog {
	t.Helper()
	rows, err := e.admin.Query(context.Background(),
		`SELECT seq,COALESCE(entry_hash,''),id,tenant_id,user_id,session_id,action,module,resource,resource_id,ip_address,user_agent,request_id,severity,created_at,
		        before_data::text,after_data::text,changes::text
		   FROM audit.audit_logs WHERE seq IS NOT NULL ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []storedLog
	for rows.Next() {
		var r storedLog
		l := &r.log
		if err := rows.Scan(&r.seq, &r.entry, &l.ID, &l.TenantID, &l.UserID, &l.SessionID, &l.Action, &l.Module, &l.Resource, &l.ResourceID, &l.IPAddress, &l.UserAgent, &l.RequestID, &l.Severity, &l.CreatedAt,
			&l.Before, &l.After, &l.Changes); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// forgeChain rehace los hashes desde la posicion from con la formula que se le da: es lo que
// haria quien escribe en la base para que una edicion no se note.
func (e *env) forgeChain(t *testing.T, from int64, version int, keyID string, hash func(seq int64, prev string, l *domain.AuditLog) string) {
	t.Helper()
	prev := ""
	for _, r := range e.storedLogs(t) {
		if r.seq < from {
			prev = r.entry
			continue
		}
		h := hash(r.seq, prev, &r.log)
		e.tamper(t, `UPDATE audit.audit_logs SET prev_hash=$1, entry_hash=$2, hash_version=$3, hash_key_id=NULLIF($4,'') WHERE seq=$5`, prev, h, version, keyID, r.seq)
		prev = h
	}
}

func v2Forger(t *testing.T, ring *crypto.MACKeyRing, keyID string) func(int64, string, *domain.AuditLog) string {
	return func(seq int64, prev string, l *domain.AuditLog) string {
		h, err := signCanonical(ring, ring.ActiveID(), auditLogCanonicalV2(keyID, seq, prev, l))
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
}

// ---- Cadena mixta: v1 y despues v2 ----

func TestUnaCadenaMixtaV1LuegoV2Verifica(t *testing.T) {
	e := setup(t)
	e.seedChain(t, 3)
	e.assertIntact(t, 3)
	before := e.storedLogs(t)

	ring := testRing(t, testKeyA, "")
	e.useKeys(ring)
	e.seedChain(t, 4)
	e.assertIntact(t, 7)

	res := e.verify(t)
	if res.Versions["1"] != 3 || res.Versions["2"] != 4 || res.Head == nil || res.Head.Seq != 7 || res.Head.HashVersion != 2 {
		t.Fatalf("%+v head=%+v", res.Versions, res.Head)
	}
	for i, r := range e.storedLogs(t)[:3] {
		if r.entry != before[i].entry {
			t.Fatalf("la fila %d, escrita en version 1, fue reescrita", i+1)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM audit.audit_logs WHERE hash_version = 1 AND hash_key_id IS NULL`); n != 3 {
		t.Fatalf("filas v1: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit.audit_logs WHERE hash_version = 2 AND hash_key_id = $1`, ring.ActiveID()); n != 4 {
		t.Fatalf("filas v2 con su llave: %d", n)
	}
}

// Una fila escrita por el servicio anterior a la migracion 07 no menciona hash_version: la base
// le da 1 y se verifica con la formula de siempre.
func TestUnaFilaSinVersionExplicitaEsV1(t *testing.T) {
	e := setup(t)
	id := uuid.New()
	e.tamper(t, `INSERT INTO audit.audit_logs (id,tenant_id,user_id,action,module,resource,ip_address,severity,created_at)
	             VALUES ($1,$2,$3,'legacy.write','identity','users','203.0.113.7','info','2026-01-02 03:04:05.123456+00')`, id, e.tenant, e.user)
	e.tamper(t, `UPDATE audit.audit_logs SET prev_hash = '', entry_hash = encode(sha256(convert_to(
		  ''||'|'||id::text||'|'||tenant_id::text||'|'||user_id::text||'|'||''||'|'||
		  action||'|'||module||'|'||resource||'|'||''||'|'||ip_address||'|'||''||'|'||
		  ''||'|'||''||'|'||''||'|'||severity||'|'||
		  rtrim(rtrim(to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US'),'0'),'.')||'Z',
		  'UTF8')),'hex') WHERE id = $1`, id)
	if n := e.count(t, `SELECT count(*) FROM audit.audit_logs WHERE hash_version = 1 AND id = $1`, id); n != 1 {
		t.Fatal("la fila no quedo en version 1 por defecto")
	}
	e.assertIntact(t, 1)
	e.useKeys(testRing(t, testKeyA, ""))
	e.seedChain(t, 2)
	e.assertIntact(t, 3)
}

// El hash v1 no cubre user_agent: se documenta para que nadie lo de por corregido en las filas
// que ya existen. Las filas v2 si lo cubren (TestManipularUnaFilaV2SeDetecta).
func TestLaVersion1SigueSinCubrirElUserAgent(t *testing.T) {
	e := setup(t)
	e.seedChain(t, 3)
	e.tamper(t, `UPDATE audit.audit_logs SET user_agent = 'otro' WHERE seq = 2`)
	e.assertIntact(t, 3)
}

// ---- Cadena v2: manipulacion ----

func TestManipularUnaFilaV2SeDetecta(t *testing.T) {
	cambios := map[string]string{
		"id":          `id = gen_random_uuid()`,
		"tenant_id":   `tenant_id = gen_random_uuid()`,
		"user_id":     `user_id = gen_random_uuid()`,
		"session_id":  `session_id = gen_random_uuid()`,
		"action":      `action = 'user.deleted'`,
		"module":      `module = 'billing'`,
		"resource":    `resource = 'roles'`,
		"resource_id": `resource_id = 'otro'`,
		"ip_address":  `ip_address = '198.51.100.99'`,
		"user_agent":  `user_agent = 'curl/8.0'`,
		"sin agente":  `user_agent = NULL`,
		"request_id":  `request_id = 'req-otro'`,
		"before_data": `before_data = '{"plan":"pro"}'`,
		"after_data":  `after_data = '{"plan":"basic"}'`,
		"changes":     `changes = '{"otro":true}'`,
		"severity":    `severity = 'critical'`,
		"created_at":  `created_at = created_at + interval '1 microsecond'`,
		"seq":         `seq = 99`,
		"prev_hash":   `prev_hash = 'deadbeef'`,
		"entry_hash":  `entry_hash = 'deadbeef'`,
		"sin hash":    `entry_hash = NULL`,
		"version":     `hash_version = 1`,
		"llave":       `hash_key_id = 'deadbeefdeadbeef'`,
		"sin llave":   `hash_key_id = NULL`,
	}
	for name, set := range cambios {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			e.useKeys(testRing(t, testKeyA, ""))
			ids := e.seedChain(t, 5)
			e.assertIntact(t, 5)
			e.tamper(t, `UPDATE audit.audit_logs SET `+set+` WHERE seq = 3`)
			res := e.verify(t)
			if res.OK || res.Reason == "" || (name != "tenant_id" && res.BrokenID == nil) {
				t.Fatalf("no se detecto: %+v", res)
			}
			if name != "id" && name != "seq" && name != "tenant_id" && *res.BrokenID != ids[2] {
				t.Fatalf("rota en %v, se esperaba %v", *res.BrokenID, ids[2])
			}
			if res.BrokenVersion == nil {
				t.Fatalf("no dice la version de la fila rota: %+v", res)
			}
		})
	}
}

func TestBorrarUnaFilaDelMedioSeNotaEnLaCadenaV2(t *testing.T) {
	e := setup(t)
	e.useKeys(testRing(t, testKeyA, ""))
	ids := e.seedChain(t, 5)
	e.tamper(t, `DELETE FROM audit.audit_logs WHERE seq = 3`)
	e.assertBroken(t, ids[3], 3)
	if res := e.verify(t); res.Reason != domain.ReasonChainBroken || res.BrokenSeq == nil || *res.BrokenSeq != 4 {
		t.Fatalf("%+v", res)
	}
}

// ---- Cadena v2: recalcular sin la clave ----

func TestRecalcularLaCadenaV2SinLaClaveSeDetecta(t *testing.T) {
	right := testRing(t, testKeyA, "")
	wrong := testRing(t, testKeyB, "")
	sha := func(seq int64, prev string, l *domain.AuditLog) string {
		sum := sha256.Sum256(auditLogCanonicalV2(right.ActiveID(), seq, prev, l))
		return hex.EncodeToString(sum[:])
	}
	casos := map[string]func(t *testing.T) func(int64, string, *domain.AuditLog) string{
		"con otra llave y el id de la buena": func(t *testing.T) func(int64, string, *domain.AuditLog) string {
			return v2Forger(t, wrong, right.ActiveID())
		},
		"con SHA-256 sin llave": func(t *testing.T) func(int64, string, *domain.AuditLog) string { return sha },
	}
	for name, forger := range casos {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			e.useKeys(right)
			ids := e.seedChain(t, 6)
			e.tamper(t, `UPDATE audit.audit_logs SET action = 'user.deleted' WHERE seq = 3`)
			e.forgeChain(t, 3, hashVersionKeyed, right.ActiveID(), forger(t))
			res := e.verify(t)
			if res.OK || res.Reason != domain.ReasonChainBroken || res.BrokenID == nil || *res.BrokenID != ids[2] {
				t.Fatalf("la cadena recalculada sin la clave paso la verificacion: %+v", res)
			}
		})
	}
}

// Control de la prueba anterior: con la clave correcta el mismo recalculo SI pasa. La clave es
// lo unico que separa a quien escribe en la base de quien puede reescribir la cadena.
func TestSoloQuienTieneLaClavePuedeRecalcularLaCadena(t *testing.T) {
	right := testRing(t, testKeyA, "")
	e := setup(t)
	e.useKeys(right)
	e.seedChain(t, 6)
	e.tamper(t, `UPDATE audit.audit_logs SET action = 'user.deleted' WHERE seq = 3`)
	e.forgeChain(t, 3, hashVersionKeyed, right.ActiveID(), v2Forger(t, right, right.ActiveID()))
	e.assertIntact(t, 6)
}

func TestBajarLasUltimasFilasAV1SeDetecta(t *testing.T) {
	right := testRing(t, testKeyA, "")
	e := setup(t)
	e.useKeys(right)
	ids := e.seedChain(t, 6)
	e.forgeChain(t, 4, hashVersionUnkeyed, "", func(_ int64, prev string, l *domain.AuditLog) string {
		return chainHash(prev, l, derefStr(l.Before), derefStr(l.After), derefStr(l.Changes), l.CreatedAt)
	})
	res := e.verify(t)
	if res.OK || res.Reason != domain.ReasonHashVersionRegression || *res.BrokenID != ids[3] {
		t.Fatalf("una fila v1 tras filas v2 debe ser una regresion: %+v", res)
	}
}

// Bajar TODA la cadena a v1 la deja consistente: el verificador de filas no tiene con que
// compararla. Es el limite de una cadena sin ancla, y lo cierra el ancla: el hash anclado era
// el v2 de la cabeza.
func TestBajarTodaLaCadenaAV1SoloLoDetectaElAncla(t *testing.T) {
	right := testRing(t, testKeyA, "")
	e := setup(t)
	e.useKeys(right)
	e.seedChain(t, 5)
	e.anchorAll(t)
	e.tamper(t, `UPDATE audit.audit_logs SET action = 'user.deleted' WHERE seq = 2`)
	e.forgeChain(t, 1, hashVersionUnkeyed, "", func(_ int64, prev string, l *domain.AuditLog) string {
		return chainHash(prev, l, derefStr(l.Before), derefStr(l.After), derefStr(l.Changes), l.CreatedAt)
	})
	if res := e.verify(t); !res.OK {
		t.Fatalf("el limite documentado dejo de darse: %+v", res)
	}
	res := e.verifyAll(t)
	if res.OK || res.Reason != domain.ReasonAnchorMismatch || res.Chain != domain.ChainAuditLogs {
		t.Fatalf("el ancla no detecto la cadena reescrita: %+v", res)
	}
}

// ---- Verificacion sin la clave y rotacion ----

func TestVerificarUnaFilaV2SinClaveEsUnFalloExplicito(t *testing.T) {
	e := setup(t)
	e.useKeys(testRing(t, testKeyA, ""))
	ids := e.seedChain(t, 3)
	e.useKeys(nil)
	res := e.verify(t)
	if res.OK || res.Reason != domain.ReasonHashKeyMissing || res.BrokenID == nil || *res.BrokenID != ids[0] || *res.BrokenVersion != 2 {
		t.Fatalf("sin clave no se puede dar por buena una fila v2: %+v", res)
	}
	if all := e.verifyAll(t); all.OK || all.Reason != domain.ReasonHashKeyMissing {
		t.Fatalf("%+v", all)
	}
}

func TestUnServicioSinClaveSigueEscribiendoV1(t *testing.T) {
	e := setup(t)
	e.seedChain(t, 3)
	if n := e.count(t, `SELECT count(*) FROM audit.audit_logs WHERE hash_version = 1 AND hash_key_id IS NULL`); n != 3 {
		t.Fatalf("filas v1: %d", n)
	}
	e.seedEvents(t, 2)
	if n := e.count(t, `SELECT count(*) FROM audit.security_events WHERE seq IS NULL AND entry_hash IS NULL`); n != 2 {
		t.Fatal("sin clave los eventos de seguridad se escriben sin cadena, como antes")
	}
	if all := e.verifyAll(t); !all.OK || all.SecurityEvents == nil || all.SecurityEvents.Checked != 0 {
		t.Fatalf("%+v", all)
	}
}

func TestRotarLaClaveNoRompeLasFilasAnteriores(t *testing.T) {
	e := setup(t)
	a := testRing(t, testKeyA, "")
	e.useKeys(a)
	e.seedChain(t, 3)

	rotating := testRing(t, testKeyB, testKeyA)
	e.useKeys(rotating)
	e.seedChain(t, 3)
	e.assertIntact(t, 6)
	if n := e.count(t, `SELECT count(DISTINCT hash_key_id) FROM audit.audit_logs`); n != 2 {
		t.Fatalf("la rotacion debia dejar dos llaves en la cadena, hay %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit.audit_logs WHERE hash_key_id = $1`, rotating.ActiveID()); n != 3 {
		t.Fatalf("las filas nuevas deben ir con la llave activa: %d", n)
	}

	// Retirar la llave vieja deja sus filas sin verificar, y lo dice con su propia causa.
	e.useKeys(testRing(t, testKeyB, ""))
	res := e.verify(t)
	if res.OK || res.Reason != domain.ReasonHashKeyUnknown || res.BrokenSeq == nil || *res.BrokenSeq != 1 {
		t.Fatalf("%+v", res)
	}
	// Y volver a la llave vieja sola tampoco verifica las nuevas.
	e.useKeys(a)
	if res := e.verify(t); res.OK || res.Reason != domain.ReasonHashKeyUnknown || *res.BrokenSeq != 4 {
		t.Fatalf("%+v", res)
	}
}

func TestElVeredictoNoFiltraLaClaveNiLosDatosDeOtraEmpresa(t *testing.T) {
	e := setup(t)
	ring := testRing(t, testKeyA, "")
	e.useKeys(ring)
	e.seedChain(t, 3)
	other := e.otherTenant()
	foreign := other.newLog("otra.empresa")
	if err := other.logs.Create(other.ctx, foreign); err != nil {
		t.Fatal(err)
	}
	e.seedChain(t, 2)
	e.tamper(t, `UPDATE audit.audit_logs SET action = 'x' WHERE id = $1`, foreign.ID)

	res := e.verifyAll(t)
	if res.OK {
		t.Fatalf("%+v", res)
	}
	if res.BrokenID != nil || res.BrokenSeq != nil {
		t.Fatalf("el veredicto nombra una fila de otra empresa: %+v", res)
	}
	body, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{testKeyA, ring.ActiveID(), foreign.ID.String(), foreign.TenantID.String()} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("el veredicto contiene %q: %s", secret, body)
		}
	}
}

// ---- Anclas ----

func TestBorrarLasUltimasFilasSoloSeNotaPorElAncla(t *testing.T) {
	e := setup(t)
	e.useKeys(testRing(t, testKeyA, ""))
	e.seedChain(t, 6)
	results := e.anchorAll(t)
	if !results[0].Anchored || results[0].HeadSeq != 6 {
		t.Fatalf("%+v", results)
	}
	if n := e.count(t, `SELECT count(*) FROM platform.event_outbox WHERE subject = 'audit.chain.anchored'`); n != 1 {
		t.Fatalf("eventos de ancla encolados: %d", n)
	}

	e.tamper(t, `DELETE FROM audit.audit_logs WHERE seq > 4`)
	// Las filas restantes enlazan bien: ningun recorrido de enlaces ve el borrado.
	e.assertIntact(t, 4)
	res := e.verifyAll(t)
	if res.OK || res.Reason != domain.ReasonHeadBehindAnchor || res.Chain != domain.ChainAuditLogs {
		t.Fatalf("el borrado de las ultimas filas no se detecto: %+v", res)
	}
	if res.Anchor == nil || res.Anchor.HeadSeq != 6 {
		t.Fatalf("el veredicto no dice contra que ancla comparo: %+v", res.Anchor)
	}

	// No se ancla la cadena mutilada: el hueco no pasa a ser la referencia.
	again, err := e.usecase().AnchorChains(e.ctx, e.tenant)
	if err != nil || again[0].Reason != domain.ReasonHeadBehindAnchor || again[0].Anchored {
		t.Fatalf("%+v %v", again, err)
	}
	if n := e.count(t, `SELECT count(*) FROM audit.chain_anchors`); n != 1 {
		t.Fatalf("anclas: %d", n)
	}
}

// Borrar las ultimas filas y seguir escribiendo deja la cadena enlazada y con una cabeza mas alta
// que el ancla: lo que delata el borrado es que ya no hay fila en la posicion anclada.
func TestBorrarYSeguirEscribiendoSeNotaPorLaPosicionAnclada(t *testing.T) {
	e := setup(t)
	e.useKeys(testRing(t, testKeyA, ""))
	e.seedChain(t, 6)
	e.anchorAll(t)
	e.tamper(t, `DELETE FROM audit.audit_logs WHERE seq > 4`)
	e.seedChain(t, 4)
	e.assertIntact(t, 8)
	res := e.verifyAll(t)
	if res.OK || res.Reason != domain.ReasonAnchorMismatch {
		t.Fatalf("%+v", res)
	}
}

func TestUnaCadenaCreciendoConservaTodasSusAnclas(t *testing.T) {
	e := setup(t)
	e.useKeys(testRing(t, testKeyA, ""))
	e.seedChain(t, 2)
	e.anchorAll(t)
	e.anchorAll(t)
	if n := e.count(t, `SELECT count(*) FROM audit.chain_anchors WHERE chain = 'audit_logs'`); n != 1 {
		t.Fatalf("una cabeza ya anclada no se repite: %d", n)
	}
	e.seedChain(t, 2)
	e.anchorAll(t)
	e.seedChain(t, 1)
	e.anchorAll(t)
	if n := e.count(t, `SELECT count(*) FROM audit.chain_anchors WHERE chain = 'audit_logs'`); n != 3 {
		t.Fatalf("anclas: %d", n)
	}
	res := e.verifyAll(t)
	if !res.OK || res.Anchor == nil || res.Anchor.HeadSeq != 5 || res.Anchor.HashVersion != 2 {
		t.Fatalf("%+v", res)
	}
	if n := e.count(t, `SELECT count(*) FROM platform.event_outbox WHERE subject = 'audit.chain.anchored'`); n != 3 {
		t.Fatalf("eventos: %d", n)
	}
	var payload string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT payload->'data'->>'head_seq' || ' ' || (payload->'data'->>'chain') || ' ' || (payload->'data'->>'tenant_id') FROM platform.event_outbox ORDER BY created_at DESC LIMIT 1`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != "5 audit_logs "+e.tenant.String() {
		t.Fatalf("payload del evento: %q", payload)
	}
}

func TestElAnclajeSirveTambienConLaCadenaV1(t *testing.T) {
	e := setup(t)
	e.seedChain(t, 4)
	e.anchorAll(t)
	e.tamper(t, `DELETE FROM audit.audit_logs WHERE seq = 4`)
	if res := e.verifyAll(t); res.OK || res.Reason != domain.ReasonHeadBehindAnchor {
		t.Fatalf("%+v", res)
	}
}

func TestLasAnclasSonDeSoloAnadirParaElRolDelServicio(t *testing.T) {
	e := setup(t)
	e.useKeys(testRing(t, testKeyA, ""))
	e.seedChain(t, 2)
	e.anchorAll(t)
	for _, sql := range []string{
		`DELETE FROM audit.chain_anchors`,
		`UPDATE audit.chain_anchors SET head_hash = 'x'`,
		`TRUNCATE audit.chain_anchors`,
	} {
		_, err := e.svc.Exec(context.Background(), sql)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%s: %v, se esperaba permission denied", sql, err)
		}
	}
	if _, err := e.svc.Exec(context.Background(), `SELECT count(*) FROM audit.chain_anchors`); err != nil {
		t.Errorf("el servicio debe poder leer las anclas: %v", err)
	}
}

func TestDosBarridosAlMismoTiempoAnclanUnaVez(t *testing.T) {
	e := setup(t)
	e.useKeys(testRing(t, testKeyA, ""))
	e.seedChain(t, 3)
	var group errgroup.Group
	for i := 0; i < 6; i++ {
		group.Go(func() error {
			_, err := e.usecase().AnchorChains(e.ctx, e.tenant)
			return err
		})
	}
	if err := group.Wait(); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM audit.chain_anchors WHERE chain = 'audit_logs'`); n != 1 {
		t.Fatalf("anclas: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM platform.event_outbox WHERE subject = 'audit.chain.anchored'`); n != 1 {
		t.Fatalf("eventos: %d", n)
	}
}

// ---- Cadena de eventos de seguridad ----

func TestLosEventosDeSeguridadSeEncadenanConLaClave(t *testing.T) {
	e := setup(t)
	e.useKeys(testRing(t, testKeyA, ""))
	e.seedEvents(t, 5)
	all := e.verifyAll(t)
	if !all.OK || all.SecurityEvents == nil || all.SecurityEvents.Checked != 5 || all.SecurityEvents.Versions["2"] != 5 {
		t.Fatalf("%+v %+v", all, all.SecurityEvents)
	}
	if n := e.count(t, `SELECT count(*) FROM audit.security_events WHERE seq IS NOT NULL AND length(entry_hash) = 64 AND hash_version = 2`); n != 5 {
		t.Fatalf("eventos encadenados: %d", n)
	}
}

func TestManipularUnEventoDeSeguridadSeDetecta(t *testing.T) {
	cambios := map[string]string{
		"id":          `id = gen_random_uuid()`,
		"tenant_id":   `tenant_id = gen_random_uuid()`,
		"user_id":     `user_id = gen_random_uuid()`,
		"sin usuario": `user_id = NULL`,
		"event_type":  `event_type = 'data_export'`,
		"ip_address":  `ip_address = '198.51.100.9'::inet`,
		"user_agent":  `user_agent = 'curl/8.0'`,
		"detail":      `detail = 'sin novedad'`,
		"risk_level":  `risk_level = 'low'`,
		"created_at":  `created_at = created_at + interval '1 microsecond'`,
		"seq":         `seq = 99`,
		"prev_hash":   `prev_hash = 'deadbeef'`,
		"entry_hash":  `entry_hash = 'deadbeef'`,
		"sin hash":    `entry_hash = NULL`,
		"version":     `hash_version = 1`,
	}
	for name, set := range cambios {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			e.useKeys(testRing(t, testKeyA, ""))
			ids := e.seedEvents(t, 5)
			e.tamper(t, `UPDATE audit.security_events SET `+set+` WHERE seq = 3`)
			res := e.verifyAll(t)
			if res.OK || res.Chain != domain.ChainSecurityEvents || res.Reason == "" {
				t.Fatalf("no se detecto: %+v", res)
			}
			if name != "id" && name != "seq" && name != "tenant_id" && (res.BrokenID == nil || *res.BrokenID != ids[2]) {
				t.Fatalf("rota en %v, se esperaba %v", res.BrokenID, ids[2])
			}
		})
	}
}

// El reconocimiento cambia a proposito: es lo unico que la cadena de eventos no cubre.
func TestReconocerUnEventoNoRompeLaCadena(t *testing.T) {
	e := setup(t)
	e.useKeys(testRing(t, testKeyA, ""))
	ids := e.seedEvents(t, 4)
	if err := e.security.Acknowledge(e.ctx, ids[1], e.user); err != nil {
		t.Fatal(err)
	}
	e.tamper(t, `UPDATE audit.security_events SET acknowledged = true, acknowledged_by = gen_random_uuid(), acknowledged_at = now() WHERE seq = 3`)
	if all := e.verifyAll(t); !all.OK {
		t.Fatalf("%+v", all)
	}
}

func TestBorrarUnEventoDeSeguridadSeNota(t *testing.T) {
	e := setup(t)
	e.useKeys(testRing(t, testKeyA, ""))
	ids := e.seedEvents(t, 5)
	e.tamper(t, `DELETE FROM audit.security_events WHERE seq = 3`)
	res := e.verifyAll(t)
	if res.OK || res.Chain != domain.ChainSecurityEvents || res.Reason != domain.ReasonChainBroken || *res.BrokenID != ids[3] {
		t.Fatalf("%+v", res)
	}
}

func TestBorrarLosUltimosEventosDeSeguridadSeNotaPorElAncla(t *testing.T) {
	e := setup(t)
	e.useKeys(testRing(t, testKeyA, ""))
	e.seedEvents(t, 5)
	if res := e.anchorAll(t); !res[1].Anchored || res[1].Chain != domain.ChainSecurityEvents {
		t.Fatalf("%+v", res)
	}
	e.tamper(t, `DELETE FROM audit.security_events WHERE seq > 3`)
	res := e.verifyAll(t)
	if res.OK || res.Chain != domain.ChainSecurityEvents || res.Reason != domain.ReasonHeadBehindAnchor {
		t.Fatalf("%+v", res)
	}
	if res.SecurityEvents == nil || res.SecurityEvents.Anchor == nil || res.SecurityEvents.Anchor.HeadSeq != 5 {
		t.Fatalf("%+v", res.SecurityEvents)
	}
}

func TestVerificarEventosFirmadosSinClaveEsUnFalloExplicito(t *testing.T) {
	e := setup(t)
	e.useKeys(testRing(t, testKeyA, ""))
	e.seedEvents(t, 2)
	e.useKeys(nil)
	res := e.verifyAll(t)
	if res.OK || res.Chain != domain.ChainSecurityEvents || res.Reason != domain.ReasonHashKeyMissing {
		t.Fatalf("%+v", res)
	}
}

func TestLosEventosSinCadenaAnterioresALaClaveNoRompenLaQueEmpieza(t *testing.T) {
	e := setup(t)
	e.seedEvents(t, 2)
	e.useKeys(testRing(t, testKeyA, ""))
	e.seedEvents(t, 3)
	all := e.verifyAll(t)
	if !all.OK || all.SecurityEvents.Checked != 3 {
		t.Fatalf("%+v %+v", all, all.SecurityEvents)
	}
}

func TestElRolDelServicioSoloPuedeReconocerEventosNoBorrarlosNiReescribirlos(t *testing.T) {
	e := setup(t)
	e.useKeys(testRing(t, testKeyA, ""))
	e.seedEvents(t, 2)
	for _, sql := range []string{
		`DELETE FROM audit.security_events`,
		`TRUNCATE audit.security_events`,
		`UPDATE audit.security_events SET risk_level = 'low'`,
		`UPDATE audit.security_events SET detail = 'x'`,
		`UPDATE audit.security_events SET seq = 1`,
		`UPDATE audit.security_events SET hash_version = 1`,
	} {
		_, err := e.svc.Exec(context.Background(), sql)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%s: %v, se esperaba permission denied", sql, err)
		}
	}
	if _, err := e.svc.Exec(context.Background(), `UPDATE audit.security_events SET acknowledged = true, acknowledged_by = $1, acknowledged_at = now()`, e.user); err != nil {
		t.Fatalf("el servicio no puede reconocer eventos: %v", err)
	}
}

// ---- Concurrencia con la cadena v2 ----

func TestEscritoresConcurrentesConClaveNoBifurcanNiRepitenPosiciones(t *testing.T) {
	e := setup(t)
	e.useKeys(testRing(t, testKeyA, ""))
	const singles, perSingle, bulkers, batches, perBatch, eventWriters, perEvent = 6, 8, 3, 3, 4, 4, 6
	logs := singles*perSingle + bulkers*batches*perBatch
	events := eventWriters * perEvent

	var group errgroup.Group
	for w := 0; w < singles; w++ {
		group.Go(func() error {
			for i := 0; i < perSingle; i++ {
				if err := e.logs.Create(e.ctx, e.fullLog("concurrent")); err != nil {
					return err
				}
			}
			return nil
		})
	}
	for w := 0; w < bulkers; w++ {
		group.Go(func() error {
			for b := 0; b < batches; b++ {
				batch := make([]*domain.AuditLog, perBatch)
				for i := range batch {
					batch[i] = e.fullLog("bulk")
				}
				if err := e.logs.BulkCreate(e.ctx, batch); err != nil {
					return err
				}
			}
			return nil
		})
	}
	for w := 0; w < eventWriters; w++ {
		group.Go(func() error {
			for i := 0; i < perEvent; i++ {
				if err := e.security.Create(e.ctx, e.newEvent("failed_login", "high", "203.0.113.9")); err != nil {
					return err
				}
			}
			return nil
		})
	}
	var sweeps sync.WaitGroup
	sweepErrs := make(chan error, 8)
	for i := 0; i < 4; i++ {
		sweeps.Add(1)
		go func() {
			defer sweeps.Done()
			for j := 0; j < 3; j++ {
				if _, err := e.usecase().AnchorChains(e.ctx, e.tenant); err != nil {
					sweepErrs <- err
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}
	if err := group.Wait(); err != nil {
		t.Fatal(err)
	}
	sweeps.Wait()
	close(sweepErrs)
	for err := range sweepErrs {
		t.Fatal(err)
	}

	e.assertIntact(t, logs)
	if n := e.count(t, `SELECT count(DISTINCT prev_hash) FROM audit.audit_logs`); n != logs {
		t.Fatalf("%d prev_hash distintos para %d filas: la cadena se bifurco", n, logs)
	}
	if n := e.count(t, `SELECT count(DISTINCT prev_hash) FROM audit.security_events`); n != events {
		t.Fatalf("%d prev_hash distintos para %d eventos: la cadena se bifurco", n, events)
	}
	e.anchorAll(t)
	all := e.verifyAll(t)
	if !all.OK || all.SecurityEvents.Checked != events || all.Versions["2"] != logs {
		t.Fatalf("%+v %+v", all, all.SecurityEvents)
	}
}
