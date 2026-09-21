package app

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-auth/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const jobToken = "cfmj1.token-de-prueba"

type fakeJobs struct {
	cred      domain.JobCredential
	err       error
	tokens    []string
	usernames []string
}

func (f *fakeJobs) Verify(_ context.Context, token, username string) (domain.JobCredential, error) {
	f.tokens = append(f.tokens, token)
	f.usernames = append(f.usernames, username)
	return f.cred, f.err
}

func jobHarness(mb *domain.Mailbox, nets ...string) (*harness, *fakeJobs) {
	h := newHarness(mb)
	jobs := &fakeJobs{}
	if mb != nil {
		jobs.cred = domain.JobCredential{TenantID: mb.TenantID, MailboxID: mb.ID, JobID: uuid.New()}
	}
	var prefixes []netip.Prefix
	for _, n := range nets {
		prefixes = append(prefixes, netip.MustParsePrefix(n))
	}
	h.uc = New(Deps{Repo: h.repo, Passwords: h.verifier, Throttle: h.throttle, Metrics: h.metrics, Logger: zap.NewNop(), Jobs: jobs, JobNetworks: prefixes})
	return h, jobs
}

func jobRequest(remoteIP string) domain.VerifyRequest {
	return domain.VerifyRequest{Username: "Ana@Empresa.PE", Password: jobToken, RemoteIP: remoteIP, Service: domain.ServiceMigration}
}

func TestCredencialDeTrabajoAbreElBuzonDesdeLaRedDelEjecutor(t *testing.T) {
	h, jobs := jobHarness(activeMailbox(), "172.22.2.0/24")
	v := h.uc.Authenticate(context.Background(), jobRequest("172.22.2.5"))
	if !v.Result.Authorized() || v.Username != h.repo.mailbox.Username || v.MailboxID != h.repo.mailbox.ID || v.TenantID != h.repo.mailbox.TenantID {
		t.Fatalf("resultado: %+v", v)
	}
	if len(jobs.tokens) != 1 || jobs.tokens[0] != jobToken || jobs.usernames[0] != "ana@empresa.pe" {
		t.Fatalf("consulta a mail-migration: %v %v", jobs.tokens, jobs.usernames)
	}
	if h.verifier.calls != 0 {
		t.Fatal("una credencial de trabajo no se compara con las contrasenas del buzon")
	}
	if len(h.repo.logins) != 1 || h.repo.logins[0].Service != domain.ServiceMigration || h.repo.logins[0].RemoteIP != "172.22.2.5" {
		t.Fatalf("rastro del inicio: %+v", h.repo.logins)
	}
	if h.throttle.successes != 1 || h.throttle.failures != 0 {
		t.Fatalf("freno: %+v", h.throttle)
	}
}

func TestCredencialDeTrabajoDesdeOtraRedSeRechazaSinPreguntarAMailMigration(t *testing.T) {
	for _, ip := range []string{"203.0.113.7", "172.22.1.5", "127.0.0.1", "no-es-una-ip", "::ffff:203.0.113.7"} {
		h, jobs := jobHarness(activeMailbox(), "172.22.2.0/24")
		v := h.uc.Authenticate(context.Background(), jobRequest(ip))
		if v.Result != domain.ResultForbiddenNetwork {
			t.Errorf("%s: resultado %s", ip, v.Result)
		}
		if len(jobs.tokens) != 0 || h.repo.finds != 0 {
			t.Errorf("%s: consulto antes de comprobar la red", ip)
		}
		if h.throttle.failures != 1 {
			t.Errorf("%s: el intento desde fuera debe alimentar el freno", ip)
		}
	}
}

func TestCredencialDeTrabajoIPv4MapeadaSeJuzgaComoIPv4(t *testing.T) {
	h, _ := jobHarness(activeMailbox(), "172.22.2.0/24")
	if v := h.uc.Authenticate(context.Background(), jobRequest("::ffff:172.22.2.9")); !v.Result.Authorized() {
		t.Fatalf("resultado: %s", v.Result)
	}
}

func TestCredencialDeTrabajoSinConfigurarSeDeniega(t *testing.T) {
	for name, build := range map[string]func() *harness{
		"sin verificador": func() *harness {
			h := newHarness(activeMailbox())
			h.uc = New(Deps{Repo: h.repo, Passwords: h.verifier, Throttle: h.throttle, Metrics: h.metrics, Logger: zap.NewNop(),
				JobNetworks: []netip.Prefix{netip.MustParsePrefix("172.22.2.0/24")}})
			return h
		},
		"sin redes": func() *harness { h, _ := jobHarness(activeMailbox()); return h },
		"nada":      func() *harness { return newHarness(activeMailbox()) },
	} {
		h := build()
		if v := h.uc.Authenticate(context.Background(), jobRequest("172.22.2.5")); v.Result != domain.ResultJobCredentialsDisabled {
			t.Errorf("%s: %s", name, v.Result)
		}
		if len(h.repo.logins) != 0 {
			t.Errorf("%s: dejo rastro de un inicio", name)
		}
	}
}

func TestCredencialDeTrabajoRechazadaPorMailMigrationAlimentaElFreno(t *testing.T) {
	h, jobs := jobHarness(activeMailbox(), "172.22.2.0/24")
	jobs.err = domain.ErrJobCredentialRejected
	if v := h.uc.Authenticate(context.Background(), jobRequest("172.22.2.5")); v.Result != domain.ResultBadPassword {
		t.Fatalf("resultado: %s", v.Result)
	}
	if h.throttle.failures != 1 || h.repo.finds != 0 || len(h.repo.logins) != 0 {
		t.Fatalf("freno %d, lecturas %d, inicios %d", h.throttle.failures, h.repo.finds, len(h.repo.logins))
	}
}

func TestUnFalloDeMailMigrationNoSeTomaPorUnRechazo(t *testing.T) {
	h, jobs := jobHarness(activeMailbox(), "172.22.2.0/24")
	jobs.err = errors.New("mail-migration caido")
	if v := h.uc.Authenticate(context.Background(), jobRequest("172.22.2.5")); v.Result != domain.ResultError {
		t.Fatalf("resultado: %s", v.Result)
	}
	if h.throttle.failures != 0 {
		t.Fatal("un fallo de infraestructura no debe bloquear al ejecutor")
	}
}

func TestCredencialDeTrabajoNoAbreUnBuzonDistintoAlDelTrabajo(t *testing.T) {
	mb := activeMailbox()
	for name, mutate := range map[string]func(c *domain.JobCredential){
		"otro buzon (borrado y recreado con el mismo nombre)": func(c *domain.JobCredential) { c.MailboxID = uuid.New() },
		"otra empresa": func(c *domain.JobCredential) { c.TenantID = uuid.New() },
	} {
		h, jobs := jobHarness(mb, "172.22.2.0/24")
		mutate(&jobs.cred)
		if v := h.uc.Authenticate(context.Background(), jobRequest("172.22.2.5")); v.Result != domain.ResultBadPassword {
			t.Errorf("%s: %s", name, v.Result)
		}
		if len(h.repo.logins) != 0 {
			t.Errorf("%s: dejo rastro de un inicio", name)
		}
	}
}

func TestCredencialDeTrabajoNoAbreUnBuzonInexistenteOSinInicioDeSesion(t *testing.T) {
	h, _ := jobHarness(nil, "172.22.2.0/24")
	if v := h.uc.Authenticate(context.Background(), jobRequest("172.22.2.5")); v.Result != domain.ResultBadPassword {
		t.Fatalf("inexistente: %s", v.Result)
	}
	for _, active := range []int16{domain.MailboxInactive, domain.MailboxReceiveOnly} {
		mb := activeMailbox()
		mb.Active = active
		h, _ := jobHarness(mb, "172.22.2.0/24")
		if v := h.uc.Authenticate(context.Background(), jobRequest("172.22.2.5")); v.Result != domain.ResultInactive {
			t.Errorf("active=%d: %s", active, v.Result)
		}
	}
}

func TestCredencialDeTrabajoNoDependeDeLosFlagsDeAccesoDelBuzon(t *testing.T) {
	mb := activeMailbox()
	mb.Access = domain.ProtocolAccess{}
	h, _ := jobHarness(mb, "172.22.2.0/24")
	if v := h.uc.Authenticate(context.Background(), jobRequest("172.22.2.5")); !v.Result.Authorized() {
		t.Fatalf("un buzon cerrado a los clientes debe poder recibir la migracion: %s", v.Result)
	}
}

func TestCredencialDeTrabajoRespetaElBloqueoPorFuerzaBruta(t *testing.T) {
	h, jobs := jobHarness(activeMailbox(), "172.22.2.0/24")
	h.throttle.blocked = true
	if v := h.uc.Authenticate(context.Background(), jobRequest("172.22.2.5")); v.Result != domain.ResultThrottled {
		t.Fatalf("resultado: %s", v.Result)
	}
	if len(jobs.tokens) != 0 {
		t.Fatal("consulto a mail-migration con el bloqueo activo")
	}
}

func TestElServicioMigrationNoSeCuelaPorLasContrasenasDelBuzon(t *testing.T) {
	// Aunque la credencial coincida con la contrasena del buzon, con service migration solo cuenta el token.
	h, _ := jobHarness(activeMailbox(), "172.22.2.0/24")
	h.uc = New(Deps{Repo: h.repo, Passwords: h.verifier, Throttle: h.throttle, Metrics: h.metrics, Logger: zap.NewNop(),
		Jobs: &fakeJobs{err: domain.ErrJobCredentialRejected}, JobNetworks: []netip.Prefix{netip.MustParsePrefix("172.22.2.0/24")}})
	req := domain.VerifyRequest{Username: "ana@empresa.pe", Password: "principal", RemoteIP: "172.22.2.5", Service: domain.ServiceMigration}
	if v := h.uc.Authenticate(context.Background(), req); v.Result.Authorized() {
		t.Fatal("la contrasena principal abrio por el canal de credenciales de trabajo")
	}
}

func TestUnaCredencialDeTrabajoNoAbreLosDemasServicios(t *testing.T) {
	h, jobs := jobHarness(activeMailbox(), "172.22.2.0/24")
	for _, service := range []string{"imap", "pop3", "smtp", "sieve", "dav", "webmail"} {
		req := jobRequest("172.22.2.5")
		req.Service = service
		if v := h.uc.Authenticate(context.Background(), req); v.Result.Authorized() {
			t.Errorf("%s: el token de trabajo abrio otro servicio", service)
		}
	}
	if len(jobs.tokens) != 0 {
		t.Fatal("los demas servicios no deben consultar a mail-migration")
	}
}
