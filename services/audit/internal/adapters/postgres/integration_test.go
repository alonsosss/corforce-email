//go:build integration

// Pruebas contra un Postgres real. Se ejecutan con:
//
//	AUDIT_TEST_DSN=postgres://... go test -tags integration ./services/audit/...
//
// Aplican las migraciones del esquema audit DOS veces y vacian sus tablas: la base debe ser
// desechable. El servicio corre bajo el rol audit_service, el de produccion, y las
// manipulaciones que se le hacen a la cadena las ejecuta un pool aparte con el rol de la
// prueba (que es lo que puede hacer quien tiene acceso a la base).
package postgres

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"
)

var migrations = []string{
	"migrations/tenant/canonical/platform/00_outbox.sql",
	"migrations/tenant/canonical/audit/01_audit.sql",
	"migrations/tenant/canonical/audit/02_security_events.sql",
	"migrations/tenant/canonical/audit/03_audit_hashchain.sql",
	"migrations/tenant/canonical/audit/04_service_role.sql",
	"migrations/tenant/canonical/audit/05_data_change_records.sql",
	"migrations/tenant/canonical/audit/06_append_only_role.sql",
	"migrations/tenant/canonical/audit/07_audit_hashchain_v2.sql",
	"migrations/tenant/canonical/audit/08_chain_anchors.sql",
	"migrations/tenant/canonical/audit/09_security_events_chain.sql",
	"migrations/tenant/canonical/audit/10_integrity_runs.sql",
}

// integrationEnv devuelve la variable de entorno que apunta a la infraestructura de la
// prueba. Sin ella la prueba se salta, salvo con INTEGRATION_REQUIRED=1 (make
// test-integration y CI): ahi es un fallo, porque un salto esconderia que no llego.
func integrationEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		if os.Getenv("INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("%s no definida con INTEGRATION_REQUIRED=1", name)
		}
		t.Skipf("%s no definida", name)
	}
	return v
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "..")
}

func applyMigration(t *testing.T, pool *pgxpool.Pool, rel string) {
	t.Helper()
	sql, err := os.ReadFile(filepath.Join(repoRoot(), rel))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("aplicar %s: %v", rel, err)
	}
}

type env struct {
	// ctx lleva el pool del servicio (rol audit_service), como en produccion.
	ctx context.Context
	// admin es el rol de la prueba: escribe en la base por fuera del servicio.
	admin *pgxpool.Pool
	svc   *pgxpool.Pool

	logs     *AuditLogRepo
	security *SecurityEventRepo
	changes  *DataChangeRepo
	summary  *AuditSummaryRepo
	anchors  *ChainAnchorRepo

	tenant uuid.UUID
	user   uuid.UUID
}

func setup(t *testing.T) *env {
	t.Helper()
	dsn := integrationEnv(t, "AUDIT_TEST_DSN")
	admin, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	for _, rel := range append(append([]string{}, migrations...), migrations...) {
		applyMigration(t, admin, rel)
	}
	if _, err := admin.Exec(context.Background(),
		`TRUNCATE audit.data_change_records, audit.audit_logs, audit.security_events, audit.chain_anchors, audit.integrity_runs, platform.event_outbox RESTART IDENTITY`); err != nil {
		t.Fatal(err)
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 24
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE audit_service")
		return err
	}
	svc, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)

	cp := &db.ContextPool{}
	return &env{
		ctx:      db.WithPool(context.Background(), svc),
		admin:    admin,
		svc:      svc,
		logs:     NewAuditLogRepo(cp, nil),
		security: NewSecurityEventRepo(cp, nil),
		changes:  NewDataChangeRepo(cp),
		summary:  NewAuditSummaryRepo(cp),
		anchors:  NewChainAnchorRepo(cp),
		tenant:   uuid.New(),
		user:     uuid.New(),
	}
}

func (e *env) newLog(action string) *domain.AuditLog {
	return &domain.AuditLog{
		ID: uuid.New(), TenantID: e.tenant, UserID: e.user,
		Action: action, Module: "identity", Resource: "users", IPAddress: "203.0.113.7", Severity: "info",
	}
}

// fullLog rellena todos los campos opcionales: la manipulacion de cualquiera debe notarse.
func (e *env) fullLog(action string) *domain.AuditLog {
	l := e.newLog(action)
	l.SessionID = ptr(uuid.New())
	l.ResourceID = ptr("res-" + action)
	l.UserAgent = ptr("Mozilla/5.0 (X11; Linux x86_64)")
	l.RequestID = ptr("req-" + action)
	l.Before = ptr(`{"plan":"basic","seats":3}`)
	l.After = ptr(`{"plan":"pro","seats":3}`)
	l.Changes = ptr(`{"plan":["basic","pro"]}`)
	return l
}

// seedChain escribe n entradas encadenadas y devuelve sus ids en orden de cadena.
func (e *env) seedChain(t *testing.T, n int) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		l := e.fullLog("action." + string(rune('a'+i)))
		if err := e.logs.Create(e.ctx, l); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, l.ID)
	}
	return ids
}

func (e *env) verify(t *testing.T) *domain.ChainIntegrity {
	t.Helper()
	res, err := e.logs.VerifyChain(e.ctx, e.tenant, domain.VerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (e *env) assertIntact(t *testing.T, checked int) {
	t.Helper()
	res := e.verify(t)
	if !res.OK || res.Checked != checked || res.BrokenID != nil {
		t.Fatalf("la cadena debia estar intacta con %d filas: %+v", checked, res)
	}
}

func (e *env) assertBroken(t *testing.T, at uuid.UUID, checked int) {
	t.Helper()
	res := e.verify(t)
	if res.OK || res.BrokenID == nil {
		t.Fatalf("la manipulacion no se detecto: %+v", res)
	}
	if *res.BrokenID != at || res.Checked != checked {
		t.Fatalf("rota en %v tras %d filas; se esperaba %v tras %d", *res.BrokenID, res.Checked, at, checked)
	}
}

// tamper ejecuta SQL con el rol de la prueba, por fuera del servicio.
func (e *env) tamper(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("manipular: %v", err)
	}
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// legacy escribe una fila como las anteriores a la cadena de hash: sin seq ni hash.
func (e *env) legacy(t *testing.T, tenant, user uuid.UUID, action, ip string, ua *string, at time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	e.tamper(t, `INSERT INTO audit.audit_logs (id,tenant_id,user_id,action,module,resource,ip_address,user_agent,severity,created_at,seq)
	             VALUES ($1,$2,$3,$4,'identity','users',$5,$6,'info',$7,NULL)`, id, tenant, user, action, ip, ua, at)
	return id
}

// ---- Cadena: escritura y verificacion ----

func TestUnaCadenaVaciaEsIntacta(t *testing.T) {
	e := setup(t)
	e.assertIntact(t, 0)
}

func TestLaCadenaEncadenaCadaFilaConLaAnterior(t *testing.T) {
	e := setup(t)
	ids := e.seedChain(t, 6)
	e.assertIntact(t, 6)

	rows, err := e.admin.Query(context.Background(), `SELECT id, seq, prev_hash, entry_hash FROM audit.audit_logs ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	prev, seen := "", 0
	for rows.Next() {
		var id uuid.UUID
		var seq int64
		var prevHash, entryHash string
		if err := rows.Scan(&id, &seq, &prevHash, &entryHash); err != nil {
			t.Fatal(err)
		}
		if id != ids[seen] || seq != int64(seen+1) {
			t.Fatalf("fila %d: id %v seq %d", seen, id, seq)
		}
		if prevHash != prev || len(entryHash) != 64 || entryHash == prevHash {
			t.Fatalf("fila %d: prev %q (esperado %q) entry %q", seen, prevHash, prev, entryHash)
		}
		prev = entryHash
		seen++
	}
	if seen != 6 {
		t.Fatalf("filas: %d", seen)
	}
}

// El hash guardado es el que da SHA-256 sobre el texto que Postgres almacena, calculado
// aqui con SQL puro y no con chainHash: la cadena no depende de como el codigo Go formatee
// el jsonb o la hora.
func TestElHashGuardadoCoincideConElCalculoIndependienteEnSQL(t *testing.T) {
	e := setup(t)
	for _, l := range []*domain.AuditLog{
		e.newLog("a.plain"),
		e.fullLog("b.full"),
		func() *domain.AuditLog {
			l := e.fullLog("c.special")
			l.Action, l.Resource = "acción|con|barras", "ruta/niño?x=1|2"
			l.Before, l.After, l.Changes = ptr(`{"b": 2,  "a": [1, 2 ,3], "á": "x|y"}`), ptr(`null`), ptr(`{}`)
			return l
		}(),
	} {
		if err := e.logs.Create(e.ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	bad := e.count(t, `
		SELECT count(*) FROM audit.audit_logs WHERE entry_hash <> encode(sha256(convert_to(
		  prev_hash||'|'||id::text||'|'||tenant_id::text||'|'||user_id::text||'|'||COALESCE(session_id::text,'')||'|'||
		  action||'|'||module||'|'||resource||'|'||COALESCE(resource_id,'')||'|'||ip_address||'|'||COALESCE(request_id,'')||'|'||
		  COALESCE(before_data::text,'')||'|'||COALESCE(after_data::text,'')||'|'||COALESCE(changes::text,'')||'|'||severity||'|'||
		  rtrim(rtrim(to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US'),'0'),'.')||'Z',
		  'UTF8')),'hex')`)
	if bad != 0 {
		t.Fatalf("%d filas cuyo hash no es el SHA-256 del formato documentado", bad)
	}
	e.assertIntact(t, 3)
}

func TestElJsonbSeVerificaEnSuFormaNormalizada(t *testing.T) {
	e := setup(t)
	l := e.newLog("x.json")
	l.Before = ptr("{  \"z\": 1,\n \"a\":   {\"k\":  [3,1,2]}  }")
	l.After = ptr(`{"a":{"k":[3,1,2]},"z":1,"z":2}`)
	l.Changes = ptr(`[]`)
	if err := e.logs.Create(e.ctx, l); err != nil {
		t.Fatal(err)
	}
	e.assertIntact(t, 1)
}

func TestUnaEntradaSinOpcionalesTambienEncadena(t *testing.T) {
	e := setup(t)
	for i := 0; i < 3; i++ {
		if err := e.logs.Create(e.ctx, e.newLog("bare")); err != nil {
			t.Fatal(err)
		}
	}
	e.assertIntact(t, 3)
}

// ---- Cadena: manipulacion de una fila ----

func TestCambiarCualquierColumnaCubiertaRompeLaCadenaEnEsaFila(t *testing.T) {
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
		"request_id":  `request_id = 'req-otro'`,
		"before_data": `before_data = '{"plan":"pro"}'`,
		"after_data":  `after_data = '{"plan":"basic"}'`,
		"changes":     `changes = '{"otro":true}'`,
		"severity":    `severity = 'critical'`,
		"created_at":  `created_at = created_at + interval '1 microsecond'`,
		"prev_hash":   `prev_hash = 'deadbeef'`,
		"entry_hash":  `entry_hash = 'deadbeef'`,
		"borrar hash": `entry_hash = NULL`,
	}
	for name, set := range cambios {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			ids := e.seedChain(t, 5)
			e.assertIntact(t, 5)
			e.tamper(t, `UPDATE audit.audit_logs SET `+set+` WHERE seq = 3`)
			// Con "id" cambiado la fila ya no tiene el id original: se identifica por posicion. Con
			// "tenant_id" cambiado la fila pasa a ser de otra empresa y el veredicto no la nombra.
			res := e.verify(t)
			if res.OK || res.Checked != 3 || (name != "tenant_id" && res.BrokenID == nil) {
				t.Fatalf("no se detecto o se detecto en otra fila: %+v", res)
			}
			if name == "tenant_id" && res.BrokenID != nil {
				t.Fatalf("el veredicto nombra una fila de otra empresa: %+v", res)
			}
			if name != "id" && name != "tenant_id" && *res.BrokenID != ids[2] {
				t.Fatalf("rota en %v, se esperaba %v", *res.BrokenID, ids[2])
			}
		})
	}
}

func TestQuitarElHashDeLaUltimaFilaTrasEditarlaNoLaEscondeDelVerificador(t *testing.T) {
	e := setup(t)
	ids := e.seedChain(t, 4)
	e.tamper(t, `UPDATE audit.audit_logs SET action = 'user.login', entry_hash = NULL, prev_hash = NULL WHERE seq = 4`)
	e.assertBroken(t, ids[3], 4)
}

func TestQuitarElSeqDeUnaFilaNoLaSacaDeLaCadena(t *testing.T) {
	e := setup(t)
	ids := e.seedChain(t, 4)
	e.tamper(t, `UPDATE audit.audit_logs SET seq = NULL WHERE seq = 2`)
	res := e.verify(t)
	if res.OK || res.BrokenID == nil {
		t.Fatalf("%+v", res)
	}
	if *res.BrokenID != ids[2] {
		t.Fatalf("la fila 3 ya no enlaza con la 1: %v", *res.BrokenID)
	}
}

func TestReescribirUnaFilaYRecalcularSoloSuHashRompeLaSiguiente(t *testing.T) {
	e := setup(t)
	ids := e.seedChain(t, 4)
	// Quien edita la accion y recalcula el hash de ESA fila deja el eslabon siguiente colgando.
	e.tamper(t, `UPDATE audit.audit_logs SET action = 'x', entry_hash = repeat('a', 64) WHERE seq = 2`)
	e.assertBroken(t, ids[1], 2)
}

// ---- Cadena: huecos, inserciones y reordenes ----

func TestBorrarUnaFilaDelMedioSeNotaEnLaSiguiente(t *testing.T) {
	e := setup(t)
	ids := e.seedChain(t, 5)
	e.tamper(t, `DELETE FROM audit.audit_logs WHERE seq = 3`)
	e.assertBroken(t, ids[3], 3)
}

func TestBorrarLaPrimeraFilaSeNotaEnLaSegunda(t *testing.T) {
	e := setup(t)
	ids := e.seedChain(t, 4)
	e.tamper(t, `DELETE FROM audit.audit_logs WHERE seq = 1`)
	e.assertBroken(t, ids[1], 1)
}

func TestBorrarVariasFilasSeguidasSeNota(t *testing.T) {
	e := setup(t)
	ids := e.seedChain(t, 8)
	e.tamper(t, `DELETE FROM audit.audit_logs WHERE seq BETWEEN 3 AND 5`)
	e.assertBroken(t, ids[5], 3)
}

func TestInsertarUnaFilaFalsaAlFinalSeNota(t *testing.T) {
	e := setup(t)
	e.seedChain(t, 3)
	forged := e.newLog("forged")
	e.tamper(t, `INSERT INTO audit.audit_logs (id,tenant_id,user_id,action,module,resource,ip_address,severity,prev_hash,entry_hash)
	             VALUES ($1,$2,$3,'forged','identity','users','203.0.113.7','info',
	                     (SELECT entry_hash FROM audit.audit_logs ORDER BY seq DESC LIMIT 1), repeat('b', 64))`,
		forged.ID, forged.TenantID, forged.UserID)
	e.assertBroken(t, forged.ID, 4)
}

func TestInsertarUnaFilaSinHashAlFinalSeNota(t *testing.T) {
	e := setup(t)
	e.seedChain(t, 3)
	forged := e.newLog("forged")
	e.tamper(t, `INSERT INTO audit.audit_logs (id,tenant_id,user_id,action,module,resource,ip_address,severity)
	             VALUES ($1,$2,$3,'forged','identity','users','203.0.113.7','info')`,
		forged.ID, forged.TenantID, forged.UserID)
	e.assertBroken(t, forged.ID, 4)
}

func TestInsertarUnaFilaFalsaEnElMedioSeNota(t *testing.T) {
	e := setup(t)
	ids := e.seedChain(t, 5)
	forged := e.newLog("forged")
	// Comparte el seq de la fila 3 y copia sus hashes con otro contenido.
	e.tamper(t, `INSERT INTO audit.audit_logs (id,tenant_id,user_id,action,module,resource,ip_address,severity,seq,prev_hash,entry_hash)
	             SELECT $1,$2,$3,'forged','identity','users','203.0.113.7','info',seq,prev_hash,entry_hash FROM audit.audit_logs WHERE seq = 3`,
		forged.ID, forged.TenantID, forged.UserID)
	res := e.verify(t)
	if res.OK || res.BrokenID == nil || (*res.BrokenID != forged.ID && *res.BrokenID != ids[2]) {
		t.Fatalf("%+v", res)
	}
}

func TestDuplicarUnaFilaExistenteSeNota(t *testing.T) {
	e := setup(t)
	e.seedChain(t, 3)
	e.tamper(t, `INSERT INTO audit.audit_logs (id,tenant_id,user_id,session_id,action,module,resource,resource_id,ip_address,request_id,before_data,after_data,changes,severity,created_at,prev_hash,entry_hash)
	             SELECT gen_random_uuid(),tenant_id,user_id,session_id,action,module,resource,resource_id,ip_address,request_id,before_data,after_data,changes,severity,created_at,prev_hash,entry_hash
	               FROM audit.audit_logs WHERE seq = 2`)
	res := e.verify(t)
	if res.OK || res.Checked != 4 {
		t.Fatalf("%+v", res)
	}
}

func TestIntercambiarElOrdenDeDosFilasSeNota(t *testing.T) {
	e := setup(t)
	ids := e.seedChain(t, 5)
	e.tamper(t, `UPDATE audit.audit_logs SET seq = CASE seq WHEN 2 THEN 3 WHEN 3 THEN 2 END WHERE seq IN (2,3)`)
	e.assertBroken(t, ids[2], 2)
}

func TestLasFilasAnterioresALaCadenaNoLaRompen(t *testing.T) {
	e := setup(t)
	other := uuid.New()
	e.legacy(t, e.tenant, other, "old.login", "198.51.100.1", nil, time.Now().Add(-48*time.Hour))
	e.seedChain(t, 3)
	e.legacy(t, e.tenant, other, "old.logout", "198.51.100.1", nil, time.Now().Add(-47*time.Hour))
	e.assertIntact(t, 3)
}

func TestLaCadenaSobreviveAUnaEscrituraFallida(t *testing.T) {
	e := setup(t)
	e.seedChain(t, 2)
	bad := e.newLog("bad")
	bad.Severity = "fatal"
	if err := e.logs.Create(e.ctx, bad); err == nil {
		t.Fatal("la severidad invalida debia rechazarse en la base")
	}
	e.seedChain(t, 2)
	e.assertIntact(t, 4)
	if n := e.count(t, `SELECT count(*) FROM audit.audit_logs WHERE id = $1`, bad.ID); n != 0 {
		t.Fatal("la fila fallida quedo escrita")
	}
}

// ---- Concurrencia ----

func TestEscritoresConcurrentesNoBifurcanNiRepitenPosiciones(t *testing.T) {
	e := setup(t)
	const singles, perSingle, bulkers, batches, perBatch = 8, 10, 4, 3, 5
	total := singles*perSingle + bulkers*batches*perBatch

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
	if err := group.Wait(); err != nil {
		t.Fatal(err)
	}

	e.assertIntact(t, total)
	if n := e.count(t, `SELECT count(DISTINCT seq) FROM audit.audit_logs`); n != total {
		t.Fatalf("%d posiciones distintas para %d filas", n, total)
	}
	if n := e.count(t, `SELECT count(DISTINCT prev_hash) FROM audit.audit_logs`); n != total {
		t.Fatalf("%d prev_hash distintos para %d filas: la cadena se bifurco", n, total)
	}
	if n := e.count(t, `SELECT count(*) FROM audit.audit_logs a WHERE a.prev_hash <> '' AND NOT EXISTS (SELECT 1 FROM audit.audit_logs b WHERE b.entry_hash = a.prev_hash)`); n != 0 {
		t.Fatalf("%d filas apuntan a un hash que no existe", n)
	}
}

// ---- Idempotencia ----

func TestRepetirUnIdNoDuplicaNiTocaLaCadena(t *testing.T) {
	e := setup(t)
	l := e.fullLog("once")
	if err := e.logs.Create(e.ctx, l); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := e.admin.QueryRow(context.Background(), `SELECT entry_hash FROM audit.audit_logs WHERE id = $1`, l.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}

	again := e.fullLog("once-again")
	again.ID = l.ID
	if err := e.logs.Create(e.ctx, again); !errors.Is(err, domain.ErrLogAlreadyRecorded) {
		t.Fatalf("%v", err)
	}
	var action, after string
	if err := e.admin.QueryRow(context.Background(), `SELECT action, entry_hash FROM audit.audit_logs WHERE id = $1`, l.ID).Scan(&action, &after); err != nil {
		t.Fatal(err)
	}
	if action != "once" || after != before {
		t.Fatalf("la reentrega modifico la fila guardada: %s %s", action, after)
	}
	e.seedChain(t, 2)
	e.assertIntact(t, 3)
}

func TestElLoteOmiteLasFilasRepetidasYSigueEncadenado(t *testing.T) {
	e := setup(t)
	first := e.fullLog("first")
	if err := e.logs.Create(e.ctx, first); err != nil {
		t.Fatal(err)
	}
	dup := e.fullLog("dup")
	dup.ID = first.ID
	fresh := e.fullLog("fresh")
	sameInBatch := e.fullLog("same-in-batch")
	sameInBatch.ID = fresh.ID
	if err := e.logs.BulkCreate(e.ctx, []*domain.AuditLog{dup, fresh, sameInBatch, e.fullLog("last")}); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM audit.audit_logs`); n != 3 {
		t.Fatalf("filas: %d, se esperaban 3", n)
	}
	e.assertIntact(t, 3)
}

func TestUnLoteConUnaFilaInvalidaNoEscribeNada(t *testing.T) {
	e := setup(t)
	e.seedChain(t, 1)
	bad := e.fullLog("bad")
	bad.Severity = "fatal"
	if err := e.logs.BulkCreate(e.ctx, []*domain.AuditLog{e.fullLog("ok-1"), e.fullLog("ok-2"), bad}); err == nil {
		t.Fatal("se esperaba un error")
	}
	e.assertIntact(t, 1)
}

func TestElLoteQuedaEnLaCadenaYSeVerifica(t *testing.T) {
	e := setup(t)
	batch := make([]*domain.AuditLog, 7)
	for i := range batch {
		batch[i] = e.fullLog("b")
	}
	if err := e.logs.BulkCreate(e.ctx, batch); err != nil {
		t.Fatal(err)
	}
	e.assertIntact(t, 7)
	if n := e.count(t, `SELECT count(*) FROM audit.audit_logs WHERE entry_hash IS NULL OR seq IS NULL`); n != 0 {
		t.Fatalf("%d filas del lote fuera de la cadena", n)
	}
	e.tamper(t, `UPDATE audit.audit_logs SET module = 'x' WHERE seq = 4`)
	e.assertBroken(t, batch[3].ID, 4)
}

func TestSinPoolEnElContextoFallaSinPanico(t *testing.T) {
	e := setup(t)
	if err := e.logs.Create(context.Background(), e.newLog("x")); err == nil {
		t.Fatal("se esperaba un error")
	}
	if _, err := e.logs.VerifyChain(context.Background(), e.tenant, domain.VerifyOptions{}); err == nil {
		t.Fatal("se esperaba un error")
	}
	if err := e.logs.BulkCreate(context.Background(), []*domain.AuditLog{e.newLog("x")}); err == nil {
		t.Fatal("se esperaba un error")
	}
}

// ---- Rol del servicio: solo anadir ----

func TestElRolDelServicioNoPuedeBorrarNiReescribirElRastro(t *testing.T) {
	e := setup(t)
	e.seedChain(t, 3)
	e.tamper(t, `INSERT INTO audit.data_change_records (tenant_id, audit_log_id, field_name) SELECT tenant_id, id, 'f' FROM audit.audit_logs WHERE seq = 1`)
	denied := []string{
		`DELETE FROM audit.audit_logs`,
		`UPDATE audit.audit_logs SET action = 'x'`,
		`UPDATE audit.audit_logs SET severity = 'info' WHERE seq = 1`,
		`TRUNCATE audit.audit_logs CASCADE`,
		`DELETE FROM audit.data_change_records`,
		`UPDATE audit.data_change_records SET field_name = 'g'`,
		`TRUNCATE audit.data_change_records`,
	}
	for _, sql := range denied {
		_, err := e.svc.Exec(context.Background(), sql)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%s: %v, se esperaba permission denied", sql, err)
		}
	}
	// Lo que el servicio si necesita: fijar los hashes de lo que acaba de insertar.
	if _, err := e.svc.Exec(context.Background(), `UPDATE audit.audit_logs SET prev_hash = prev_hash, entry_hash = entry_hash WHERE seq = 1`); err != nil {
		t.Fatalf("el servicio no puede fijar los hashes: %v", err)
	}
	e.assertIntact(t, 3)
}

func TestElRolDelServicioMantieneLoQueNecesita(t *testing.T) {
	e := setup(t)
	for _, sql := range []string{
		`SELECT count(*) FROM audit.audit_logs`,
		`SELECT count(*) FROM audit.security_events`,
		`SELECT count(*) FROM audit.data_change_records`,
		`SELECT nextval('audit.audit_logs_seq')`,
	} {
		if _, err := e.svc.Exec(context.Background(), sql); err != nil {
			t.Errorf("%s: %v", sql, err)
		}
	}
}
