package app

import (
	"context"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-auth/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Los hashes del repositorio falso son etiquetas: el verificador falso compara
// "hash de X" con la contrasena X, sin bcrypt.
const (
	hashMain  = "hash:principal"
	hashApp   = "hash:movil"
	hashDummy = "hash:ficticio"
)

type fakeVerifier struct{ calls int }

func (f *fakeVerifier) Verify(hash, password string) bool {
	f.calls++
	return hash == "hash:"+password
}
func (f *fakeVerifier) DummyHash() string { return hashDummy }

type fakeRepo struct {
	mailbox      *domain.Mailbox
	appPasswords []domain.AppPassword
	appProtocol  domain.Protocol
	finds        int
	touched      []uuid.UUID
	logins       []domain.Login
}

func (r *fakeRepo) FindByUsername(_ context.Context, username string) (*domain.Mailbox, error) {
	r.finds++
	if r.mailbox == nil || r.mailbox.Username != username {
		return nil, domain.ErrNotFound
	}
	return r.mailbox, nil
}

func (r *fakeRepo) ListAppPasswords(_ context.Context, _ uuid.UUID, p domain.Protocol) ([]domain.AppPassword, error) {
	if p != r.appProtocol {
		return nil, nil
	}
	return r.appPasswords, nil
}

func (r *fakeRepo) TouchAppPassword(_ context.Context, id uuid.UUID) error {
	r.touched = append(r.touched, id)
	return nil
}

func (r *fakeRepo) RecordLogin(_ context.Context, l domain.Login) error {
	r.logins = append(r.logins, l)
	return nil
}

func (r *fakeRepo) RecentLogins(context.Context, uuid.UUID, string, int) ([]domain.Login, error) {
	return r.logins, nil
}

type fakeThrottle struct {
	blocked   bool
	failures  int
	successes int
}

func (t *fakeThrottle) Blocked(context.Context, string, string) bool { return t.blocked }
func (t *fakeThrottle) Failure(context.Context, string, string)      { t.failures++ }
func (t *fakeThrottle) Success(context.Context, string, string)      { t.successes++ }

type fakeMetrics struct{ last domain.Result }

func (m *fakeMetrics) Attempt(_ string, r domain.Result) { m.last = r }

func activeMailbox() *domain.Mailbox {
	return &domain.Mailbox{
		ID:           uuid.New(),
		TenantID:     uuid.New(),
		Username:     "ana@empresa.pe",
		PasswordHash: hashMain,
		Active:       domain.MailboxActive,
		Access:       domain.ProtocolAccess{IMAP: true, POP3: true, SMTP: true, Sieve: true},
	}
}

type harness struct {
	uc       *UseCase
	repo     *fakeRepo
	verifier *fakeVerifier
	throttle *fakeThrottle
	metrics  *fakeMetrics
}

func newHarness(mb *domain.Mailbox) *harness {
	h := &harness{
		repo:     &fakeRepo{mailbox: mb},
		verifier: &fakeVerifier{},
		throttle: &fakeThrottle{},
		metrics:  &fakeMetrics{},
	}
	h.uc = New(Deps{Repo: h.repo, Passwords: h.verifier, Throttle: h.throttle, Metrics: h.metrics, Logger: zap.NewNop()})
	return h
}

func request(password, service string) domain.VerifyRequest {
	return domain.VerifyRequest{Username: "Ana@Empresa.PE", Password: password, RemoteIP: "203.0.113.7", Service: service}
}

func TestVerifyContrasenaCorrectaAutoriza(t *testing.T) {
	h := newHarness(activeMailbox())

	got := h.uc.Verify(context.Background(), request("principal", "imap"))

	if got != domain.ResultOK {
		t.Fatalf("resultado = %s, se esperaba ok", got)
	}
	if h.throttle.successes != 1 || h.throttle.failures != 0 {
		t.Fatalf("freno: successes=%d failures=%d", h.throttle.successes, h.throttle.failures)
	}
	if len(h.repo.logins) != 1 {
		t.Fatalf("se esperaba un registro en sasl_logins, hay %d", len(h.repo.logins))
	}
	l := h.repo.logins[0]
	if l.Username != "ana@empresa.pe" || l.Service != "imap" || l.RemoteIP != "203.0.113.7" || l.AppPasswordID != nil {
		t.Fatalf("registro inesperado: %+v", l)
	}
	if h.metrics.last != domain.ResultOK {
		t.Fatalf("metrica = %s", h.metrics.last)
	}
}

func TestVerifyBuzonSoloRecibeDeniega(t *testing.T) {
	mb := activeMailbox()
	mb.Active = domain.MailboxReceiveOnly
	h := newHarness(mb)

	if got := h.uc.Verify(context.Background(), request("principal", "imap")); got != domain.ResultInactive {
		t.Fatalf("resultado = %s, se esperaba inactive", got)
	}
	if len(h.repo.logins) != 0 {
		t.Fatal("un buzon que no entra no debe registrar inicio")
	}
	// La contrasena era correcta: no cuenta como fuerza bruta.
	if h.throttle.failures != 0 {
		t.Fatal("una contrasena correcta sobre un buzon inactivo no alimenta el freno")
	}
}

func TestVerifyFlagDeProtocoloApagadoDeniega(t *testing.T) {
	mb := activeMailbox()
	mb.Access.POP3 = false
	h := newHarness(mb)

	if got := h.uc.Verify(context.Background(), request("principal", "pop3")); got != domain.ResultNoAccess {
		t.Fatalf("resultado = %s, se esperaba no_access", got)
	}
	if got := h.uc.Verify(context.Background(), request("principal", "imap")); got != domain.ResultOK {
		t.Fatalf("imap sigue permitido, resultado = %s", got)
	}
}

func TestVerifyContrasenaDeAplicacionConFlag(t *testing.T) {
	mb := activeMailbox()
	h := newHarness(mb)
	appID := uuid.New()
	h.repo.appPasswords = []domain.AppPassword{{ID: appID, Name: "movil", PasswordHash: hashApp}}
	h.repo.appProtocol = domain.ProtocolSMTP

	// submission comparte flag con smtp.
	if got := h.uc.Verify(context.Background(), request("movil", "submission")); got != domain.ResultOK {
		t.Fatalf("resultado = %s, se esperaba ok", got)
	}
	if len(h.repo.touched) != 1 || h.repo.touched[0] != appID {
		t.Fatalf("last_used_at no se actualizo para %s: %v", appID, h.repo.touched)
	}
	if len(h.repo.logins) != 1 || h.repo.logins[0].AppPasswordID == nil || *h.repo.logins[0].AppPasswordID != appID {
		t.Fatalf("el registro debe llevar la contrasena de aplicacion: %+v", h.repo.logins)
	}

	// La misma contrasena por un protocolo para el que no esta habilitada no entra.
	if got := h.uc.Verify(context.Background(), request("movil", "imap")); got != domain.ResultBadPassword {
		t.Fatalf("resultado por imap = %s, se esperaba bad_password", got)
	}
	if h.throttle.failures != 1 {
		t.Fatalf("el fallo debe alimentar el freno: failures=%d", h.throttle.failures)
	}
}

func TestVerifyUsuarioInexistenteComparaIgualmente(t *testing.T) {
	h := newHarness(nil)

	if got := h.uc.Verify(context.Background(), request("loquesea", "imap")); got != domain.ResultBadPassword {
		t.Fatalf("resultado = %s, se esperaba bad_password", got)
	}
	if h.verifier.calls != 1 {
		t.Fatalf("debe compararse contra el hash ficticio exactamente una vez, calls=%d", h.verifier.calls)
	}
	if h.throttle.failures != 1 {
		t.Fatalf("el intento debe contar en el freno: failures=%d", h.throttle.failures)
	}
}

func TestVerifyFrenoActivoNoConsultaLaBase(t *testing.T) {
	h := newHarness(activeMailbox())
	h.throttle.blocked = true

	if got := h.uc.Verify(context.Background(), request("principal", "imap")); got != domain.ResultThrottled {
		t.Fatalf("resultado = %s, se esperaba throttled", got)
	}
	if h.repo.finds != 0 {
		t.Fatal("con el freno activo no debe consultarse el buzon")
	}
	if h.verifier.calls != 0 {
		t.Fatal("con el freno activo no debe compararse ninguna contrasena")
	}
}

func TestVerifyServicioDesconocidoDeniega(t *testing.T) {
	h := newHarness(activeMailbox())

	if got := h.uc.Verify(context.Background(), request("principal", "dav")); got != domain.ResultUnknownService {
		t.Fatalf("resultado = %s, se esperaba unknown_service", got)
	}
	if h.repo.finds != 0 {
		t.Fatal("un servicio desconocido se deniega sin consultar la base")
	}
}

func TestVerifyContrasenaIncorrectaAlimentaElFreno(t *testing.T) {
	h := newHarness(activeMailbox())

	if got := h.uc.Verify(context.Background(), request("otra", "imap")); got != domain.ResultBadPassword {
		t.Fatalf("resultado = %s, se esperaba bad_password", got)
	}
	if h.throttle.failures != 1 || h.throttle.successes != 0 {
		t.Fatalf("freno: failures=%d successes=%d", h.throttle.failures, h.throttle.successes)
	}
}

func TestVerifyWebmailExigeImapYSmtp(t *testing.T) {
	cases := map[string]struct {
		imap, smtp bool
		want       domain.Result
	}{
		"ambos":    {true, true, domain.ResultOK},
		"sin smtp": {true, false, domain.ResultNoAccess},
		"sin imap": {false, true, domain.ResultNoAccess},
	}
	for name, c := range cases {
		mb := activeMailbox()
		mb.Access.IMAP, mb.Access.SMTP = c.imap, c.smtp
		h := newHarness(mb)
		if got := h.uc.Verify(context.Background(), request("principal", "webmail")); got != c.want {
			t.Fatalf("%s: resultado = %s, se esperaba %s", name, got, c.want)
		}
	}
}

func TestVerifyWebmailNoAceptaContrasenasDeAplicacion(t *testing.T) {
	h := newHarness(activeMailbox())
	// Aunque hubiera una contrasena de aplicacion habilitada para el protocolo, el webmail
	// no la consulta: solo entra la principal.
	h.repo.appPasswords = []domain.AppPassword{{ID: uuid.New(), Name: "movil", PasswordHash: hashApp}}
	h.repo.appProtocol = domain.ProtocolWebmail

	if got := h.uc.Verify(context.Background(), request("movil", "webmail")); got != domain.ResultBadPassword {
		t.Fatalf("resultado = %s, se esperaba bad_password", got)
	}
	if h.throttle.failures != 1 {
		t.Fatalf("el intento debe alimentar el freno: failures=%d", h.throttle.failures)
	}
	if got := h.uc.Verify(context.Background(), request("principal", "webmail")); got != domain.ResultOK {
		t.Fatalf("la contrasena principal abre el webmail, resultado = %s", got)
	}
	if len(h.repo.logins) != 1 || h.repo.logins[0].Service != "webmail" {
		t.Fatalf("el inicio del webmail se registra con su servicio: %+v", h.repo.logins)
	}
}

func TestAuthenticateDevuelveElNombreSoloSiAutoriza(t *testing.T) {
	mb := activeMailbox()
	mb.DisplayName = "Ana Perez"
	h := newHarness(mb)

	if v := h.uc.Authenticate(context.Background(), request("principal", "webmail")); v.Result != domain.ResultOK || v.DisplayName != "Ana Perez" {
		t.Fatalf("verificacion inesperada: %+v", v)
	}
	if v := h.uc.Authenticate(context.Background(), request("otra", "webmail")); v.Result != domain.ResultBadPassword || v.DisplayName != "" {
		t.Fatalf("un rechazo no debe llevar el nombre: %+v", v)
	}
}

func TestVerifySinFrenoConfiguradoFunciona(t *testing.T) {
	repo := &fakeRepo{mailbox: activeMailbox()}
	uc := New(Deps{Repo: repo, Passwords: &fakeVerifier{}, Logger: zap.NewNop()})

	if got := uc.Verify(context.Background(), request("principal", "imap")); got != domain.ResultOK {
		t.Fatalf("resultado = %s, se esperaba ok", got)
	}
}
