package app

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-migration/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/ports"
	"github.com/google/uuid"
)

func TestCrearGuardaElTrabajoCifradoYAnunciaElEvento(t *testing.T) {
	f := newFixture(t, nil)
	in := validInput()
	job, err := f.uc.Create(context.Background(), f.tenant, f.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != domain.StatusPending || job.MailboxUsername != "ana@acme.test" || job.RequestedBy != f.actor {
		t.Fatalf("trabajo: %+v", job)
	}
	if job.SourcePasswordEnc != nil {
		t.Fatal("el trabajo devuelto lleva la credencial")
	}
	stored := f.repo.Jobs[job.ID]
	if bytes.Contains(stored.SourcePasswordEnc, []byte(in.Source.Password)) && !bytes.HasPrefix(stored.SourcePasswordEnc, apptest.CipherMark) {
		t.Fatal("la credencial se guardo sin cifrar")
	}
	if len(stored.SourcePasswordEnc) == 0 {
		t.Fatal("no se guardo la credencial cifrada")
	}
	if !reflect.DeepEqual(f.events.Log, []string{"created"}) {
		t.Fatalf("eventos: %v", f.events.Log)
	}
	if !reflect.DeepEqual(f.resolver.Asked, []string{"imap.origen.example"}) {
		t.Fatalf("no resolvio el origen: %v", f.resolver.Asked)
	}
}

func TestCrearSinClaveDelEjecutorResponde503(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.RunnerConfigured = false })
	if _, err := f.uc.Create(context.Background(), f.tenant, f.actor, validInput()); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
	if len(f.repo.Jobs) != 0 || len(f.resolver.Asked) != 0 {
		t.Fatal("sin ejecutor no debe guardarse ni resolverse nada")
	}
	if _, err := f.uc.Claim(context.Background(), "runner-1"); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("Claim sin clave: %v", err)
	}
}

func TestCrearRechazaOrigenesInternosResueltosPorDNS(t *testing.T) {
	casos := map[string][]string{
		"privada":        {"10.1.1.1"},
		"loopback":       {"127.0.0.1"},
		"metadatos":      {"169.254.169.254"},
		"ipv6 loopback":  {"::1"},
		"mezcla":         {"93.184.216.34", "192.168.0.10"},
		"ipv4 en ipv6":   {"::ffff:10.0.0.1"},
		"enlace local":   {"fe80::1"},
		"cgnat":          {"100.64.0.9"},
		"metadatos ipv6": {"fd00:ec2::254"},
	}
	for name, raw := range casos {
		f := newFixture(t, nil)
		f.resolver.Addrs = nil
		for _, a := range raw {
			f.resolver.Addrs = append(f.resolver.Addrs, netip.MustParseAddr(a))
		}
		_, err := f.uc.Create(context.Background(), f.tenant, f.actor, validInput())
		if !errors.Is(err, domain.ErrHostNotAllowed) {
			t.Errorf("%s: err = %v", name, err)
		}
		if len(f.repo.Jobs) != 0 || len(f.events.Log) != 0 {
			t.Errorf("%s: se guardo algo pese al rechazo", name)
		}
	}
}

func TestCrearRechazaUnOrigenQueNoResuelve(t *testing.T) {
	f := newFixture(t, nil)
	f.resolver.Addrs, f.resolver.Err = nil, errors.New("no such host")
	if _, err := f.uc.Create(context.Background(), f.tenant, f.actor, validInput()); !errors.Is(err, domain.ErrHostUnresolvable) {
		t.Fatalf("err = %v", err)
	}
}

func TestCrearRechazaLoInvalidoAntesDeTocarLaRedNiLaBase(t *testing.T) {
	f := newFixture(t, nil)
	in := validInput()
	in.Source.Host = "169.254.169.254"
	if _, err := f.uc.Create(context.Background(), f.tenant, f.actor, in); !errors.Is(err, domain.ErrHostNotAllowed) {
		t.Fatalf("IP literal interna: %v", err)
	}
	in = validInput()
	in.Source.Port = 25
	if _, err := f.uc.Create(context.Background(), f.tenant, f.actor, in); !errors.Is(err, domain.ErrInvalidPort) {
		t.Fatalf("puerto: %v", err)
	}
	if len(f.resolver.Asked) != 0 {
		t.Fatalf("resolvio un origen ya invalido: %v", f.resolver.Asked)
	}
}

func TestCrearValidaElBuzonDestino(t *testing.T) {
	f := newFixture(t, nil)
	f.mailboxes.Err = domain.ErrMailboxNotFound
	if _, err := f.uc.Create(context.Background(), f.tenant, f.actor, validInput()); !errors.Is(err, domain.ErrMailboxNotFound) {
		t.Fatalf("buzon ajeno: %v", err)
	}
	f.mailboxes.Err = nil
	f.mailboxes.Ref.Active = false
	if _, err := f.uc.Create(context.Background(), f.tenant, f.actor, validInput()); !errors.Is(err, domain.ErrMailboxInactive) {
		t.Fatalf("buzon inactivo: %v", err)
	}
	if len(f.repo.Jobs) != 0 {
		t.Fatal("se guardo un trabajo pese al rechazo")
	}
}

func TestCrearRespetaElLimiteDeLaEmpresaYUnTrabajoPorBuzon(t *testing.T) {
	f := newFixture(t, nil)
	first := validInput()
	if _, err := f.uc.Create(context.Background(), f.tenant, f.actor, first); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.Create(context.Background(), f.tenant, f.actor, first); !errors.Is(err, domain.ErrJobAlreadyActive) {
		t.Fatalf("mismo buzon: %v", err)
	}
	if _, err := f.uc.Create(context.Background(), f.tenant, f.actor, validInput()); err != nil {
		t.Fatalf("segundo buzon: %v", err)
	}
	if _, err := f.uc.Create(context.Background(), f.tenant, f.actor, validInput()); !errors.Is(err, domain.ErrTenantLimitReached) {
		t.Fatalf("tercero: %v", err)
	}
	other := uuid.New()
	if _, err := f.uc.Create(context.Background(), other, f.actor, validInput()); err != nil {
		t.Fatalf("otra empresa no comparte el limite: %v", err)
	}
	meta, err := f.uc.Meta(context.Background(), f.tenant)
	if err != nil || meta.ActiveJobs != 2 || meta.MaxActiveJobs != 2 || !meta.Configured {
		t.Fatalf("meta: %+v %v", meta, err)
	}
	if !reflect.DeepEqual(meta.SourcePorts, []int{143, 993}) || meta.DefaultTLSForPort[993] != domain.TLSImplicit || meta.DefaultTLSForPort[143] != domain.TLSStartTLS {
		t.Fatalf("meta de origen: %+v", meta)
	}
}

func TestCancelarUnTrabajoPendienteBorraLaCredencialYAnuncia(t *testing.T) {
	f := newFixture(t, nil)
	job, _ := f.uc.Create(context.Background(), f.tenant, f.actor, validInput())
	got, err := f.uc.Cancel(context.Background(), f.tenant, f.actor, job.ID)
	if err != nil || got.Status != domain.StatusCancelled {
		t.Fatalf("cancelar: %v %+v", err, got)
	}
	if f.repo.Jobs[job.ID].SourcePasswordEnc != nil {
		t.Fatal("la credencial sobrevive a la cancelacion")
	}
	if !reflect.DeepEqual(f.events.Log, []string{"created", "finished:cancelled"}) {
		t.Fatalf("eventos: %v", f.events.Log)
	}
	if _, err := f.uc.Cancel(context.Background(), f.tenant, f.actor, job.ID); !errors.Is(err, domain.ErrNotCancellable) {
		t.Fatalf("segunda cancelacion: %v", err)
	}
	if _, err := f.uc.Cancel(context.Background(), uuid.New(), f.actor, job.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("otra empresa: %v", err)
	}
}

func TestCancelarUnoEnCursoSoloPideLaCancelacionUnaVez(t *testing.T) {
	f := newFixture(t, nil)
	job, _ := f.uc.Create(context.Background(), f.tenant, f.actor, validInput())
	if _, err := f.uc.Claim(context.Background(), "runner-1"); err != nil {
		t.Fatal(err)
	}
	got, err := f.uc.Cancel(context.Background(), f.tenant, f.actor, job.ID)
	if err != nil || got.Status != domain.StatusRunning || got.CancelRequestedAt == nil {
		t.Fatalf("cancelar en curso: %v %+v", err, got)
	}
	f.now = f.now.Add(time.Minute)
	if _, err := f.uc.Cancel(context.Background(), f.tenant, f.actor, job.ID); err != nil {
		t.Fatal(err)
	}
	want := []string{"created", "started", "cancel_requested"}
	if !reflect.DeepEqual(f.events.Log, want) {
		t.Fatalf("eventos %v, se esperaba %v", f.events.Log, want)
	}
}

func TestReclamarEntregaLaCredencialDescifradaUnaVezYMarcaElTrabajo(t *testing.T) {
	f := newFixture(t, nil)
	in := validInput()
	created, _ := f.uc.Create(context.Background(), f.tenant, f.actor, in)
	claimed, err := f.uc.Claim(context.Background(), " runner-1 ")
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if claimed.SourcePassword != in.Source.Password || claimed.SourceUsername != "ana@origen.example" ||
		claimed.DestinationUsername != "ana@acme.test" || claimed.JobID != created.ID || claimed.TenantID != f.tenant ||
		claimed.Attempt != 1 || claimed.LeaseSeconds != 90 || claimed.SourceHost != "imap.origen.example" {
		t.Fatalf("reclamo: %+v", claimed)
	}
	stored := f.repo.Jobs[created.ID]
	if stored.Status != domain.StatusRunning || stored.RunnerID != "runner-1" {
		t.Fatalf("estado: %+v", stored)
	}
	again, err := f.uc.Claim(context.Background(), "runner-2")
	if err != nil || again != nil {
		t.Fatalf("un trabajo en curso no se entrega dos veces: %v %v", again, err)
	}
}

func TestReclamarUsaLaPistaYSoloRecorreLasEmpresasCadaIntervalo(t *testing.T) {
	f := newFixture(t, nil)
	other := uuid.New()
	f.tenants.IDs = []uuid.UUID{other, f.tenant}
	ctx := context.Background()

	if c, err := f.uc.Claim(ctx, "r"); err != nil || c != nil {
		t.Fatalf("sin trabajo: %v %v", c, err)
	}
	if !reflect.DeepEqual(f.tenants.Visited, []uuid.UUID{other, f.tenant}) {
		t.Fatalf("el primer reclamo debe recorrer las empresas: %v", f.tenants.Visited)
	}
	f.tenants.Visited = nil
	if c, _ := f.uc.Claim(ctx, "r"); c != nil {
		t.Fatal("no hay trabajo")
	}
	if len(f.tenants.Visited) != 0 {
		t.Fatalf("recorrio de nuevo antes del intervalo: %v", f.tenants.Visited)
	}

	// Un trabajo nuevo en esta instancia deja su pista: se reclama sin recorrer nada.
	if _, err := f.uc.Create(ctx, f.tenant, f.actor, validInput()); err != nil {
		t.Fatal(err)
	}
	if c, err := f.uc.Claim(ctx, "r"); err != nil || c == nil {
		t.Fatalf("con pista: %v %v", c, err)
	}
	if len(f.tenants.Visited) != 0 {
		t.Fatalf("la pista no evito el recorrido: %v", f.tenants.Visited)
	}

	// Pasado el intervalo, un trabajo creado por otra instancia (sin pista) se encuentra recorriendo.
	f.repo.Jobs[uuid.New()] = &domain.Job{
		ID: uuid.New(), TenantID: other, MailboxID: uuid.New(), Status: domain.StatusPending, SourcePasswordEnc: append(append([]byte{}, apptest.CipherMark...), "x"...),
		MailboxUsername: "b@acme.test", CreatedAt: f.now,
	}
	f.now = f.now.Add(time.Minute)
	c, err := f.uc.Claim(ctx, "r")
	if err != nil || c == nil || c.TenantID != other {
		t.Fatalf("recorrido: %v %v", c, err)
	}
}

func TestReclamarConCredencialIlegibleFallaElTrabajoSinEntregarla(t *testing.T) {
	f := newFixture(t, nil)
	f.uc.cipher = apptest.Cipher{FailDecrypt: true}
	created, _ := f.uc.Create(context.Background(), f.tenant, f.actor, validInput())
	claimed, err := f.uc.Claim(context.Background(), "runner-1")
	if err != nil || claimed != nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	stored := f.repo.Jobs[created.ID]
	if stored.Status != domain.StatusFailed || stored.LastError == nil || stored.LastError.Code != domain.CodeCredentialUnreadable {
		t.Fatalf("estado: %+v", stored)
	}
	if stored.SourcePasswordEnc != nil {
		t.Fatal("la credencial sobrevive al fallo")
	}
	if got := f.events.Log[len(f.events.Log)-1]; got != "finished:failed" {
		t.Fatalf("eventos: %v", f.events.Log)
	}
}

func TestReclamarCierraLosTrabajosPerdidosDeLaEmpresa(t *testing.T) {
	f := newFixture(t, nil)
	lost := domain.Job{ID: uuid.New(), TenantID: f.tenant, Status: domain.StatusFailed, LastError: domain.RunnerLostError()}
	f.repo.ExpireOut = []domain.Job{lost}
	if c, err := f.uc.Claim(context.Background(), "r"); err != nil || c != nil {
		t.Fatalf("claim: %v %v", c, err)
	}
	if !reflect.DeepEqual(f.events.Log, []string{"finished:failed"}) {
		t.Fatalf("eventos: %v", f.events.Log)
	}
}

func TestReclamarRechazaUnEjecutorSinNombre(t *testing.T) {
	f := newFixture(t, nil)
	for _, id := range []string{"", "   ", strings.Repeat("r", 101)} {
		if _, err := f.uc.Claim(context.Background(), id); !errors.Is(err, domain.ErrInvalidRunner) {
			t.Errorf("%q: %v", id, err)
		}
	}
}

func claimOne(t *testing.T, f *fixture) (*ClaimedJob, *domain.Job) {
	t.Helper()
	created, err := f.uc.Create(context.Background(), f.tenant, f.actor, validInput())
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := f.uc.Claim(context.Background(), "runner-1")
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	return claimed, created
}

func TestLatidoGuardaElProgresoYAvisaLaCancelacion(t *testing.T) {
	f := newFixture(t, nil)
	claimed, created := claimOne(t, f)
	in := HeartbeatInput{LeaseID: claimed.LeaseID, Phase: "initial", Progress: domain.Progress{MessagesCopied: 5, FoldersTotal: 2}}
	res, err := f.uc.Heartbeat(context.Background(), f.tenant, created.ID, in)
	if err != nil || res.Cancel || res.LeaseSeconds != 90 {
		t.Fatalf("latido: %+v %v", res, err)
	}
	stored := f.repo.Jobs[created.ID]
	if stored.Progress.MessagesCopied != 5 || stored.Phase != domain.PhaseInitial || stored.Progress.Folders == nil {
		t.Fatalf("progreso: %+v", stored)
	}
	if _, err := f.uc.Cancel(context.Background(), f.tenant, f.actor, created.ID); err != nil {
		t.Fatal(err)
	}
	if res, _ := f.uc.Heartbeat(context.Background(), f.tenant, created.ID, in); !res.Cancel {
		t.Fatal("el latido no aviso de la cancelacion")
	}
}

func TestLatidoRechazaLoInvalido(t *testing.T) {
	f := newFixture(t, nil)
	claimed, created := claimOne(t, f)
	ctx := context.Background()
	if _, err := f.uc.Heartbeat(ctx, f.tenant, created.ID, HeartbeatInput{LeaseID: claimed.LeaseID, Phase: "otra"}); !errors.Is(err, domain.ErrInvalidPhase) {
		t.Errorf("fase: %v", err)
	}
	if _, err := f.uc.Heartbeat(ctx, f.tenant, created.ID, HeartbeatInput{LeaseID: claimed.LeaseID, Phase: "initial", Progress: domain.Progress{BytesCopied: -1}}); !errors.Is(err, domain.ErrInvalidProgress) {
		t.Errorf("progreso: %v", err)
	}
	if _, err := f.uc.Heartbeat(ctx, f.tenant, created.ID, HeartbeatInput{LeaseID: uuid.New(), Phase: "initial"}); !errors.Is(err, domain.ErrLeaseLost) {
		t.Errorf("lease ajeno: %v", err)
	}
	f.tenants.ForErr = domain.ErrTenantUnknown
	if _, err := f.uc.Heartbeat(ctx, f.tenant, created.ID, HeartbeatInput{LeaseID: claimed.LeaseID, Phase: "initial"}); !errors.Is(err, domain.ErrTenantUnknown) {
		t.Errorf("empresa desconocida: %v", err)
	}
}

func TestCompletarBorraLaCredencialYSaneaElMensaje(t *testing.T) {
	f := newFixture(t, nil)
	claimed, created := claimOne(t, f)
	in := CompleteInput{
		LeaseID: claimed.LeaseID, Outcome: "failed", ErrorCode: "source_auth_failed",
		ErrorMessage: "NO login failed para clave-de-origen-123\x00",
		Progress:     domain.Progress{MessagesCopied: 4, MessagesFailed: 1},
	}
	job, err := f.uc.Complete(context.Background(), f.tenant, created.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != domain.StatusFailed || job.LastError == nil || job.LastError.Code != domain.CodeSourceAuthFailed {
		t.Fatalf("estado: %+v", job)
	}
	if strings.Contains(job.LastError.Message, "clave-de-origen-123") {
		t.Fatalf("el mensaje repite la contrasena: %q", job.LastError.Message)
	}
	if f.repo.Jobs[created.ID].SourcePasswordEnc != nil || f.repo.Jobs[created.ID].LeaseID != nil {
		t.Fatal("la credencial o el lease sobreviven al cierre")
	}
	if got := f.events.Log[len(f.events.Log)-1]; got != "finished:failed" {
		t.Fatalf("eventos: %v", f.events.Log)
	}
	if _, err := f.uc.Complete(context.Background(), f.tenant, created.ID, in); !errors.Is(err, domain.ErrLeaseLost) {
		t.Fatalf("cerrar dos veces: %v", err)
	}
}

func TestCompletarTodosLosResultadosBorranLaCredencial(t *testing.T) {
	for outcome, want := range map[string]domain.Status{"succeeded": domain.StatusSucceeded, "cancelled": domain.StatusCancelled} {
		f := newFixture(t, nil)
		claimed, created := claimOne(t, f)
		job, err := f.uc.Complete(context.Background(), f.tenant, created.ID, CompleteInput{LeaseID: claimed.LeaseID, Outcome: outcome})
		if err != nil || job.Status != want {
			t.Fatalf("%s: %v %+v", outcome, err, job)
		}
		if f.repo.Jobs[created.ID].SourcePasswordEnc != nil {
			t.Fatalf("%s: la credencial sobrevive", outcome)
		}
	}
	f := newFixture(t, nil)
	claimed, created := claimOne(t, f)
	if _, err := f.uc.Complete(context.Background(), f.tenant, created.ID, CompleteInput{LeaseID: claimed.LeaseID, Outcome: "paused"}); !errors.Is(err, domain.ErrInvalidOutcome) {
		t.Fatalf("resultado invalido: %v", err)
	}
}

func TestCompletarConUnLeaseAjenoNoCierraElTrabajo(t *testing.T) {
	f := newFixture(t, nil)
	_, created := claimOne(t, f)
	if _, err := f.uc.Complete(context.Background(), f.tenant, created.ID, CompleteInput{LeaseID: uuid.New(), Outcome: "succeeded"}); !errors.Is(err, domain.ErrLeaseLost) {
		t.Fatalf("err = %v", err)
	}
	if f.repo.Jobs[created.ID].Status != domain.StatusRunning {
		t.Fatal("un ejecutor sin el lease cerro el trabajo")
	}
}

func TestListarYVerNoDevuelvenLaCredencial(t *testing.T) {
	f := newFixture(t, nil)
	created, _ := f.uc.Create(context.Background(), f.tenant, f.actor, validInput())
	jobs, total, err := f.uc.List(context.Background(), f.tenant, ports.ListFilter{}, ports.Page{Limit: 10})
	if err != nil || total != 1 || len(jobs) != 1 || jobs[0].SourcePasswordEnc != nil {
		t.Fatalf("listar: %v %d %+v", err, total, jobs)
	}
	got, err := f.uc.Get(context.Background(), f.tenant, created.ID)
	if err != nil || got.SourcePasswordEnc != nil {
		t.Fatalf("ver: %v %+v", err, got)
	}
	if _, err := f.uc.Get(context.Background(), uuid.New(), created.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("otra empresa: %v", err)
	}
}

func TestNormalizePage(t *testing.T) {
	if page, per, p := NormalizePage(0, 0); page != 1 || per != DefaultPageSize || p.Offset != 0 {
		t.Errorf("defecto: %d %d %+v", page, per, p)
	}
	if _, per, p := NormalizePage(3, 10_000); per != MaxPageSize || p.Offset != 2*MaxPageSize {
		t.Errorf("recorta al maximo: %d %+v", per, p)
	}
}
