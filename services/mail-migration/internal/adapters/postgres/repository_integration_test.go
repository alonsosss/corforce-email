//go:build integration

package postgres

// Prueba de integracion contra un Postgres real. Se ejecuta con:
//
//	MAIL_MIGRATION_TEST_DSN=postgres://user:pass@localhost:5432/db?sslmode=disable \
//	  go test -tags integration ./services/mail-migration/internal/adapters/postgres/
//
// Aplica las migraciones canonicas del servicio dos veces (idempotencia) y ejercita el reclamo
// concurrente, el borrado de la credencial en todo estado final, el limite por empresa y el
// aislamiento entre empresas sobre una base desechable.

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/pkg/db"
	outboxadapter "github.com/alonsosss/corforce-email/services/mail-migration/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

func setup(t *testing.T) (context.Context, *Repository, *pgxpool.Pool) {
	t.Helper()
	dsn := integrationEnv(t, "MAIL_MIGRATION_TEST_DSN")
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("conectar: %v", err)
	}
	t.Cleanup(pool.Close)

	canonical := filepath.Join("..", "..", "..", "..", "..", "migrations", "tenant", "canonical")
	files, err := filepath.Glob(filepath.Join(canonical, "mail-migration", "*.sql"))
	if err != nil || len(files) < 2 {
		t.Fatalf("migraciones del servicio: %v %v", files, err)
	}
	sort.Strings(files)
	files = append([]string{filepath.Join(canonical, "platform", "00_outbox.sql")}, files...)
	for i := 0; i < 2; i++ {
		for _, f := range files {
			sqlBytes, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("leer migracion %s: %v", f, err)
			}
			if _, err := pool.Exec(ctx, string(sqlBytes)); err != nil {
				t.Fatalf("aplicar migracion %s (pasada %d): %v", filepath.Base(f), i+1, err)
			}
		}
	}
	return db.WithPool(ctx, pool), NewRepository(&db.ContextPool{}), pool
}

var now = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

func newJob(tenant uuid.UUID) *domain.Job {
	return &domain.Job{
		ID: uuid.New(), TenantID: tenant, MailboxID: uuid.New(), MailboxUsername: "ana@acme.test",
		SourceHost: "imap.origen.example", SourcePort: 993, SourceTLS: domain.TLSImplicit, SourceUsername: "ana@origen.example",
		SourcePasswordEnc: []byte("cifrado-opaco"), Status: domain.StatusPending, RequestedBy: uuid.New(),
		Progress:  domain.Progress{Folders: []domain.FolderProgress{}},
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
}

func insert(t *testing.T, ctx context.Context, r *Repository, j *domain.Job, max int) error {
	t.Helper()
	return r.Transact(ctx, func(ctx context.Context) error { return r.Insert(ctx, j, max) })
}

func claimParams(runner string) ports.ClaimParams {
	return ports.ClaimParams{RunnerID: runner, LeaseID: uuid.New(), Now: now, LeaseUntil: now.Add(90 * time.Second), MaxAttempts: 3}
}

func storedPassword(t *testing.T, ctx context.Context, r *Repository, id uuid.UUID) []byte {
	t.Helper()
	var enc []byte
	if err := r.pool.QueryRow(ctx, `SELECT source_password_enc FROM mail_migration.jobs WHERE id = $1`, id).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	return enc
}

func TestInsertarLeerYAislarEmpresas(t *testing.T) {
	ctx, repo, _ := setup(t)
	tenant, other := uuid.New(), uuid.New()
	j := newJob(tenant)
	if err := insert(t, ctx, repo, j, 5); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := repo.Get(ctx, tenant, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusPending || got.MailboxUsername != "ana@acme.test" || got.SourceHost != "imap.origen.example" ||
		got.SourceTLS != domain.TLSImplicit || !bytes.Equal(got.SourcePasswordEnc, []byte("cifrado-opaco")) || got.LastError != nil ||
		got.Progress.Folders == nil || got.Attempt != 0 || got.LeaseID != nil {
		t.Fatalf("leido: %+v", got)
	}

	if _, err := repo.Get(ctx, other, j.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("otra empresa ve el trabajo: %v", err)
	}
	if jobs, total, err := repo.List(ctx, other, ports.ListFilter{}, ports.Page{Limit: 10}); err != nil || total != 0 || len(jobs) != 0 {
		t.Errorf("otra empresa lista el trabajo: %v %d %d", err, total, len(jobs))
	}
	if n, _ := repo.CountActive(ctx, other); n != 0 {
		t.Errorf("otra empresa cuenta el trabajo: %d", n)
	}
	if c, err := repo.Claim(ctx, other, claimParams("r")); err != nil || c != nil {
		t.Errorf("otra empresa reclama el trabajo: %v %v", c, err)
	}
	if _, err := repo.RequestCancel(ctx, other, j.ID, now); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("otra empresa cancela el trabajo: %v", err)
	}
	if got, _ := repo.Get(ctx, tenant, j.ID); got.Status != domain.StatusPending {
		t.Errorf("el intento ajeno cambio el trabajo: %s", got.Status)
	}

	// Un trabajo vive en una sola empresa: la reclamada mueve solo el suyo.
	c, err := repo.Claim(ctx, tenant, claimParams("runner-1"))
	if err != nil || c == nil || c.ID != j.ID {
		t.Fatalf("Claim: %v %v", c, err)
	}
	lease := *c.LeaseID
	if _, err := repo.Heartbeat(ctx, other, j.ID, ports.HeartbeatParams{LeaseID: lease, Phase: domain.PhaseInitial, Now: now, LeaseUntil: now}); !errors.Is(err, domain.ErrLeaseLost) {
		t.Errorf("otra empresa da latido: %v", err)
	}
	if _, err := repo.Finish(ctx, other, j.ID, ports.FinishParams{LeaseID: lease, Status: domain.StatusSucceeded, Now: now}); !errors.Is(err, domain.ErrLeaseLost) {
		t.Errorf("otra empresa cierra el trabajo: %v", err)
	}
	if enc := storedPassword(t, ctx, repo, j.ID); len(enc) == 0 {
		t.Error("un cierre ajeno borro la credencial")
	}
}

func TestElLimiteDeLaEmpresaAguantaAltasSimultaneas(t *testing.T) {
	ctx, repo, _ := setup(t)
	tenant := uuid.New()
	const limit, attempts = 3, 12
	var wg sync.WaitGroup
	results := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- insert(t, ctx, repo, newJob(tenant), limit)
		}()
	}
	wg.Wait()
	close(results)
	ok, limited := 0, 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrTenantLimitReached):
			limited++
		default:
			t.Errorf("error inesperado: %v", err)
		}
	}
	if ok != limit || limited != attempts-limit {
		t.Fatalf("altas %d y rechazos %d, se esperaban %d y %d", ok, limited, limit, attempts-limit)
	}
	if n, _ := repo.CountActive(ctx, tenant); n != limit {
		t.Fatalf("activos %d", n)
	}
}

func TestUnSoloTrabajoActivoPorBuzon(t *testing.T) {
	ctx, repo, _ := setup(t)
	tenant := uuid.New()
	first := newJob(tenant)
	if err := insert(t, ctx, repo, first, 5); err != nil {
		t.Fatal(err)
	}
	dup := newJob(tenant)
	dup.MailboxID = first.MailboxID
	if err := insert(t, ctx, repo, dup, 5); !errors.Is(err, domain.ErrJobAlreadyActive) {
		t.Fatalf("mismo buzon: %v", err)
	}
	// Cuando termina, el buzon queda libre para otra migracion.
	c, _ := repo.Claim(ctx, tenant, claimParams("r"))
	if _, err := repo.Finish(ctx, tenant, first.ID, ports.FinishParams{LeaseID: *c.LeaseID, Status: domain.StatusSucceeded, Now: now}); err != nil {
		t.Fatal(err)
	}
	if err := insert(t, ctx, repo, dup, 5); err != nil {
		t.Fatalf("buzon libre: %v", err)
	}
}

func TestReclamoConcurrenteEntregaCadaTrabajoUnaSolaVez(t *testing.T) {
	ctx, repo, _ := setup(t)
	tenant := uuid.New()
	const jobs, runners = 6, 16
	want := map[uuid.UUID]bool{}
	for i := 0; i < jobs; i++ {
		j := newJob(tenant)
		j.CreatedAt = j.CreatedAt.Add(time.Duration(i) * time.Millisecond)
		if err := insert(t, ctx, repo, j, 100); err != nil {
			t.Fatal(err)
		}
		want[j.ID] = true
	}
	var mu sync.Mutex
	claimed := map[uuid.UUID]int{}
	var wg sync.WaitGroup
	for i := 0; i < runners; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			j, err := repo.Claim(ctx, tenant, claimParams("runner"))
			if err != nil {
				t.Errorf("Claim: %v", err)
				return
			}
			if j == nil {
				return
			}
			mu.Lock()
			claimed[j.ID]++
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if len(claimed) != jobs {
		t.Fatalf("reclamados %d de %d", len(claimed), jobs)
	}
	for id, n := range claimed {
		if n != 1 || !want[id] {
			t.Errorf("trabajo %s entregado %d veces", id, n)
		}
	}
	if j, err := repo.Claim(ctx, tenant, claimParams("tarde")); err != nil || j != nil {
		t.Fatalf("no queda trabajo: %v %v", j, err)
	}
	var running int
	_ = repo.pool.QueryRow(ctx, `SELECT count(*) FROM mail_migration.jobs WHERE tenant_id = $1 AND status = 'running' AND attempt = 1`, tenant).Scan(&running)
	if running != jobs {
		t.Fatalf("en curso %d", running)
	}
}

func TestLaCredencialSeBorraEnTodoEstadoFinal(t *testing.T) {
	ctx, repo, pool := setup(t)
	tenant := uuid.New()

	for _, status := range []domain.Status{domain.StatusSucceeded, domain.StatusFailed, domain.StatusCancelled} {
		j := newJob(tenant)
		if err := insert(t, ctx, repo, j, 100); err != nil {
			t.Fatal(err)
		}
		c, _ := repo.Claim(ctx, tenant, claimParams("r"))
		if c.ID != j.ID && c.ID == uuid.Nil {
			t.Fatal("claim")
		}
		if enc := storedPassword(t, ctx, repo, c.ID); len(enc) == 0 {
			t.Fatal("en curso debe conservarla")
		}
		var jobErr *domain.JobError
		if status == domain.StatusFailed {
			jobErr = &domain.JobError{Code: domain.CodeSourceAuthFailed, Message: "login fallido"}
		}
		done, err := repo.Finish(ctx, tenant, c.ID, ports.FinishParams{LeaseID: *c.LeaseID, Status: status, Progress: domain.Progress{MessagesCopied: 4}, Error: jobErr, Now: now})
		if err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		if done.SourcePasswordEnc != nil || storedPassword(t, ctx, repo, c.ID) != nil || done.FinishedAt == nil || done.LeaseID != nil {
			t.Fatalf("%s: credencial, lease o fecha: %+v", status, done)
		}
		if done.Progress.MessagesCopied != 4 || (jobErr != nil && (done.LastError == nil || done.LastError.Code != domain.CodeSourceAuthFailed)) {
			t.Fatalf("%s: %+v", status, done)
		}
		if _, err := repo.Finish(ctx, tenant, c.ID, ports.FinishParams{LeaseID: *c.LeaseID, Status: domain.StatusSucceeded, Now: now}); !errors.Is(err, domain.ErrLeaseLost) {
			t.Fatalf("un trabajo cerrado no se cierra otra vez: %v", err)
		}
	}

	// Cancelar uno pendiente.
	pending := newJob(tenant)
	if err := insert(t, ctx, repo, pending, 100); err != nil {
		t.Fatal(err)
	}
	cancelled, err := repo.RequestCancel(ctx, tenant, pending.ID, now)
	if err != nil || cancelled.Status != domain.StatusCancelled || cancelled.SourcePasswordEnc != nil || storedPassword(t, ctx, repo, pending.ID) != nil {
		t.Fatalf("cancelar pendiente: %v %+v", err, cancelled)
	}
	if _, err := repo.RequestCancel(ctx, tenant, pending.ID, now); !errors.Is(err, domain.ErrNotCancellable) {
		t.Fatalf("segunda cancelacion: %v", err)
	}

	// La base misma impide un estado final con credencial.
	live := newJob(tenant)
	if err := insert(t, ctx, repo, live, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE mail_migration.jobs SET status = 'failed', finished_at = now() WHERE id = $1`, live.ID); err == nil {
		t.Fatal("la base admitio un trabajo fallido que conserva la credencial")
	}
	if _, err := pool.Exec(ctx, `UPDATE mail_migration.jobs SET status = 'succeeded' WHERE id = $1`, live.ID); err == nil {
		t.Fatal("la base admitio un estado final sin fecha")
	}
}

func TestLatidoCancelacionYLeaseVencido(t *testing.T) {
	ctx, repo, _ := setup(t)
	tenant := uuid.New()
	j := newJob(tenant)
	if err := insert(t, ctx, repo, j, 100); err != nil {
		t.Fatal(err)
	}
	c, _ := repo.Claim(ctx, tenant, claimParams("runner-1"))
	if c.Attempt != 1 || c.RunnerID != "runner-1" || c.Phase != domain.PhaseInitial || c.StartedAt == nil {
		t.Fatalf("reclamado: %+v", c)
	}
	hb := ports.HeartbeatParams{LeaseID: *c.LeaseID, Phase: domain.PhaseCatchup, Now: now.Add(10 * time.Second), LeaseUntil: now.Add(100 * time.Second),
		Progress: domain.Progress{MessagesCopied: 8, Folders: []domain.FolderProgress{{Name: "INBOX", MessagesCopied: 8}}}}
	cancel, err := repo.Heartbeat(ctx, tenant, j.ID, hb)
	if err != nil || cancel {
		t.Fatalf("latido: %v %v", cancel, err)
	}
	got, _ := repo.Get(ctx, tenant, j.ID)
	if got.Phase != domain.PhaseCatchup || got.Progress.MessagesCopied != 8 || len(got.Progress.Folders) != 1 || got.Progress.Folders[0].Name != "INBOX" {
		t.Fatalf("progreso: %+v", got)
	}
	bad := hb
	bad.LeaseID = uuid.New()
	if _, err := repo.Heartbeat(ctx, tenant, j.ID, bad); !errors.Is(err, domain.ErrLeaseLost) {
		t.Fatalf("lease ajeno: %v", err)
	}

	// Pedir la cancelacion de uno en curso no lo cierra: lo avisa el siguiente latido.
	running, err := repo.RequestCancel(ctx, tenant, j.ID, now.Add(20*time.Second))
	if err != nil || running.Status != domain.StatusRunning || running.CancelRequestedAt == nil || running.SourcePasswordEnc == nil {
		t.Fatalf("cancelar en curso: %v %+v", err, running)
	}
	if cancel, err := repo.Heartbeat(ctx, tenant, j.ID, hb); err != nil || !cancel {
		t.Fatalf("el latido no aviso de la cancelacion: %v %v", cancel, err)
	}
	// Con la cancelacion pedida y el lease vencido, ExpireLost lo cierra como cancelado y borra la credencial.
	expired, err := repo.ExpireLost(ctx, tenant, now.Add(10*time.Minute), 3)
	if err != nil || len(expired) != 1 || expired[0].Status != domain.StatusCancelled || expired[0].SourcePasswordEnc != nil {
		t.Fatalf("ExpireLost cancelado: %v %+v", err, expired)
	}
	if storedPassword(t, ctx, repo, j.ID) != nil {
		t.Fatal("la credencial sobrevive")
	}
}

func TestUnTrabajoPerdidoSeReintentaYAlAgotarLosIntentosFalla(t *testing.T) {
	ctx, repo, _ := setup(t)
	tenant := uuid.New()
	j := newJob(tenant)
	if err := insert(t, ctx, repo, j, 100); err != nil {
		t.Fatal(err)
	}
	at := now
	for attempt := 1; attempt <= 3; attempt++ {
		p := claimParams("runner")
		p.Now, p.LeaseUntil = at, at.Add(90*time.Second)
		c, err := repo.Claim(ctx, tenant, p)
		if err != nil || c == nil || c.Attempt != attempt {
			t.Fatalf("intento %d: %v %+v", attempt, err, c)
		}
		if len(c.SourcePasswordEnc) == 0 {
			t.Fatalf("intento %d: el reintento necesita la credencial", attempt)
		}
		// Mientras el lease sigue vigente nadie mas lo toma.
		if other, _ := repo.Claim(ctx, tenant, ports.ClaimParams{RunnerID: "otro", LeaseID: uuid.New(), Now: at.Add(time.Second), LeaseUntil: at.Add(time.Minute), MaxAttempts: 3}); other != nil {
			t.Fatalf("intento %d: lease vigente reclamado por otro", attempt)
		}
		at = at.Add(5 * time.Minute)
	}
	// Tercer lease vencido y sin intentos: no se reclama, se cierra como fallido.
	p := claimParams("runner")
	p.Now, p.LeaseUntil = at, at.Add(90*time.Second)
	if c, err := repo.Claim(ctx, tenant, p); err != nil || c != nil {
		t.Fatalf("un trabajo sin intentos no se reclama: %v %v", c, err)
	}
	expired, err := repo.ExpireLost(ctx, tenant, at, 3)
	if err != nil || len(expired) != 1 {
		t.Fatalf("ExpireLost: %v %v", expired, err)
	}
	lost := expired[0]
	if lost.Status != domain.StatusFailed || lost.LastError == nil || lost.LastError.Code != domain.CodeRunnerLost || lost.SourcePasswordEnc != nil {
		t.Fatalf("perdido: %+v", lost)
	}
	if again, _ := repo.ExpireLost(ctx, tenant, at, 3); len(again) != 0 {
		t.Fatalf("se cerro dos veces: %v", again)
	}
}

func TestListarFiltraPaginaYOrdena(t *testing.T) {
	ctx, repo, _ := setup(t)
	tenant := uuid.New()
	mailbox := uuid.New()
	var ids []uuid.UUID
	for i := 0; i < 5; i++ {
		j := newJob(tenant)
		j.CreatedAt = j.CreatedAt.Add(time.Duration(i) * time.Second)
		if err := insert(t, ctx, repo, j, 100); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
	}
	// Dos trabajos ya cancelados del mismo buzon: el indice de un activo por buzon no los alcanza.
	if _, err := repo.pool.Exec(ctx,
		`UPDATE mail_migration.jobs SET status = 'cancelled', finished_at = now(), source_password_enc = NULL, mailbox_id = $1
 WHERE id = ANY($2)`, mailbox, []uuid.UUID{ids[0], ids[2]}); err != nil {
		t.Fatal(err)
	}
	all, total, err := repo.List(ctx, tenant, ports.ListFilter{}, ports.Page{Limit: 3})
	if err != nil || total != 5 || len(all) != 3 || all[0].ID != ids[4] {
		t.Fatalf("listar: %v %d %d", err, total, len(all))
	}
	page2, _, _ := repo.List(ctx, tenant, ports.ListFilter{}, ports.Page{Offset: 3, Limit: 3})
	if len(page2) != 2 || page2[1].ID != ids[0] {
		t.Fatalf("segunda pagina: %+v", page2)
	}
	byMailbox, total, _ := repo.List(ctx, tenant, ports.ListFilter{MailboxID: &mailbox}, ports.Page{Limit: 10})
	if total != 2 || len(byMailbox) != 2 {
		t.Fatalf("por buzon: %d", total)
	}
	cancelled := domain.StatusCancelled
	if got, total, _ := repo.List(ctx, tenant, ports.ListFilter{Status: &cancelled}, ports.Page{Limit: 10}); total != 2 || len(got) != 2 {
		t.Fatalf("por estado: %d", total)
	}
	done := domain.StatusSucceeded
	if none, total, _ := repo.List(ctx, tenant, ports.ListFilter{Status: &done}, ports.Page{Limit: 10}); total != 0 || len(none) != 0 {
		t.Fatalf("por estado sin filas: %d", total)
	}
}

func TestElRolDeServicioSoloAlcanzaSuEsquemaYLaOutbox(t *testing.T) {
	ctx, repo, _ := setup(t)
	var jobsDML, outbox, ddl bool
	if err := repo.pool.QueryRow(ctx,
		`SELECT has_table_privilege('mail_migration_service', 'mail_migration.jobs', 'SELECT,INSERT,UPDATE,DELETE'),
 has_table_privilege('mail_migration_service', 'platform.event_outbox', 'INSERT'),
 has_schema_privilege('mail_migration_service', 'mail_migration', 'CREATE')`).Scan(&jobsDML, &outbox, &ddl); err != nil {
		t.Fatal(err)
	}
	if !jobsDML || !outbox || ddl {
		t.Fatalf("permisos: dml=%v outbox=%v ddl=%v", jobsDML, outbox, ddl)
	}
}

// El recorrido completo con la aplicacion real sobre Postgres: la credencial viaja cifrada con
// MAIL_ENCRYPTION_KEY, el ejecutor la recibe descifrada una vez, y al cerrar no queda nada de ella.
// Los eventos se encolan en la outbox de la misma transaccion.
func TestDeExtremoAExtremoConCifradoRealYOutbox(t *testing.T) {
	ctx, repo, _ := setup(t)
	t.Setenv("MIGRATION_IT_KEY", strings.Repeat("ab", 32))
	ring, err := crypto.LoadKeyRing("MIGRATION_IT_KEY", "MIGRATION_IT_KEY_OLD")
	if err != nil {
		t.Fatal(err)
	}
	tenant, actor := uuid.New(), uuid.New()
	const password = "una-clave-de-origen-muy-especifica"
	uc := app.New(app.Deps{
		Repo: repo, Tx: repo, Mailboxes: &apptest.Mailboxes{Ref: ports.MailboxRef{Username: "ana@acme.test", Active: true}},
		Resolver: &apptest.Resolver{Addrs: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}, Cipher: ring,
		Tenants: &apptest.Tenants{IDs: []uuid.UUID{tenant}}, Events: outboxadapter.NewPublisher(repo.pool),
		Config: app.Config{RunnerConfigured: true, MaxActivePerTenant: 2, Lease: 90 * time.Second, MaxAttempts: 3, SweepInterval: time.Second,
			Source: domain.SourcePolicy{Ports: []int{143, 993}}},
	})

	job, err := uc.Create(ctx, tenant, actor, app.CreateInput{MailboxID: uuid.New(), Source: domain.Source{
		Host: "imap.origen.example", Port: 993, TLS: domain.TLSImplicit, Username: "ana@origen.example", Password: password}})
	if err != nil {
		t.Fatal(err)
	}
	enc := storedPassword(t, ctx, repo, job.ID)
	if len(enc) == 0 || bytes.Contains(enc, []byte(password)) {
		t.Fatal("la credencial no esta cifrada en la base")
	}
	var leaked int
	_ = repo.pool.QueryRow(ctx, `SELECT count(*) FROM mail_migration.jobs j WHERE j.tenant_id = $1 AND (j.*)::text LIKE '%' || $2 || '%'`, tenant, password).Scan(&leaked)
	if leaked != 0 {
		t.Fatal("alguna columna guarda la contrasena en claro")
	}

	claimed, err := uc.Claim(ctx, "runner-1")
	if err != nil || claimed == nil || claimed.SourcePassword != password || claimed.DestinationUsername != "ana@acme.test" {
		t.Fatalf("reclamo: %+v %v", claimed, err)
	}
	if _, err := uc.Heartbeat(ctx, tenant, job.ID, app.HeartbeatInput{LeaseID: claimed.LeaseID, Phase: "initial", Progress: domain.Progress{MessagesCopied: 3}}); err != nil {
		t.Fatal(err)
	}
	done, err := uc.Complete(ctx, tenant, job.ID, app.CompleteInput{LeaseID: claimed.LeaseID, Outcome: "failed", ErrorCode: "source_auth_failed",
		ErrorMessage: "NO [AUTHENTICATIONFAILED] " + password, Progress: domain.Progress{MessagesCopied: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if done.LastError == nil || strings.Contains(done.LastError.Message, password) {
		t.Fatalf("el mensaje repite la contrasena: %+v", done.LastError)
	}
	if storedPassword(t, ctx, repo, job.ID) != nil {
		t.Fatal("la credencial sobrevive al cierre")
	}
	var text int
	_ = repo.pool.QueryRow(ctx, `SELECT count(*) FROM mail_migration.jobs j WHERE j.tenant_id = $1 AND (j.*)::text LIKE '%' || $2 || '%'`, tenant, password).Scan(&text)
	if text != 0 {
		t.Fatal("la contrasena quedo en alguna columna tras el cierre")
	}

	rows, err := repo.pool.Query(ctx, `SELECT subject, payload::text FROM platform.event_outbox WHERE tenant_id = $1 ORDER BY created_at, subject`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	subjects := map[string]bool{}
	for rows.Next() {
		var subject, payload string
		if err := rows.Scan(&subject, &payload); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(payload, password) || strings.Contains(payload, "ana@origen.example") {
			t.Errorf("%s filtra datos de origen: %s", subject, payload)
		}
		subjects[subject] = true
	}
	for _, want := range []string{outboxadapter.SubjectCreated, outboxadapter.SubjectStarted, outboxadapter.SubjectFailed} {
		if !subjects[want] {
			t.Errorf("falta el evento %s en la outbox: %v", want, subjects)
		}
	}
}
