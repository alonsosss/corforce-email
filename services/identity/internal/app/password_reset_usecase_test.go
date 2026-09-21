package app

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// resetNow es el reloj fijo de estas pruebas: ninguna depende de la fecha real.
var resetNow = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

const resetBaseURL = "https://app.example.test"

type rsUsers struct {
	ports.UserRepository
	byEmail map[string]*domain.User
	lookups int
}

func (s *rsUsers) GetByEmail(_ context.Context, tenant uuid.UUID, email string) (*domain.User, error) {
	s.lookups++
	if u, ok := s.byEmail[email]; ok && u.TenantID == tenant {
		return u, nil
	}
	return nil, domain.ErrUserNotFound
}

func (s *rsUsers) GetByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	for _, u := range s.byEmail {
		if u.ID == id {
			return u, nil
		}
	}
	return nil, domain.ErrUserNotFound
}

type rsTenants struct {
	ports.TenantRepository
	byEmail map[string]uuid.UUID
	lookups int
}

func (t *rsTenants) GetIDByEmail(_ context.Context, email string) (uuid.UUID, error) {
	t.lookups++
	if id, ok := t.byEmail[email]; ok {
		return id, nil
	}
	return uuid.Nil, domain.ErrTenantNotFound
}

type rsResets struct {
	ports.PasswordResetRepository
	created     []*domain.PasswordResetToken
	invalidated int
	byHash      map[string]*domain.PasswordResetToken
	// recent es la respuesta de RequestedSince y since lo que se le pregunto.
	recent    bool
	recentErr error
	since     time.Time
}

func (r *rsResets) Create(_ context.Context, tok *domain.PasswordResetToken) error {
	r.created = append(r.created, tok)
	return nil
}
func (r *rsResets) InvalidateForUser(context.Context, uuid.UUID) error { r.invalidated++; return nil }
func (r *rsResets) RequestedSince(_ context.Context, _ uuid.UUID, since time.Time) (bool, error) {
	r.since = since
	return r.recent, r.recentErr
}
func (r *rsResets) GetByTokenHash(_ context.Context, hash string) (*domain.PasswordResetToken, error) {
	if tok, ok := r.byHash[hash]; ok {
		return tok, nil
	}
	return nil, domain.ErrResetTokenInvalid
}

type rsMail struct {
	tenant            uuid.UUID
	to, subject, body string
}

type rsMailer struct {
	configured bool
	sent       []rsMail
}

func (m *rsMailer) Send(_ context.Context, tenant uuid.UUID, to, subject, body string) error {
	m.sent = append(m.sent, rsMail{tenant, to, subject, body})
	return nil
}
func (m *rsMailer) Configured() bool { return m.configured }

type rsQueue struct {
	got  []ports.PasswordResetRequest
	full bool
}

func (q *rsQueue) Enqueue(req ports.PasswordResetRequest) bool {
	if q.full {
		return false
	}
	q.got = append(q.got, req)
	return true
}

type rsAudit struct{ entries []*domain.AuditEntry }

func (a *rsAudit) Log(_ context.Context, e *domain.AuditEntry) error {
	a.entries = append(a.entries, e)
	return nil
}

type resetFixture struct {
	uc      *PasswordResetUseCase
	users   *rsUsers
	tenants *rsTenants
	resets  *rsResets
	mailer  *rsMailer
	queue   *rsQueue
	audit   *rsAudit
	known   *domain.User
}

func newResetFixture(t *testing.T, publicBaseURL string) *resetFixture {
	t.Helper()
	tenant := uuid.New()
	known := &domain.User{ID: uuid.New(), TenantID: tenant, Email: "ana@example.test", FirstName: "Ana", Status: domain.UserStatusActive}
	inactive := &domain.User{ID: uuid.New(), TenantID: tenant, Email: "baja@example.test", Status: domain.UserStatusInactive}
	f := &resetFixture{
		users:   &rsUsers{byEmail: map[string]*domain.User{known.Email: known, inactive.Email: inactive}},
		tenants: &rsTenants{byEmail: map[string]uuid.UUID{known.Email: tenant, inactive.Email: tenant}},
		resets:  &rsResets{byHash: map[string]*domain.PasswordResetToken{}},
		mailer:  &rsMailer{configured: true},
		queue:   &rsQueue{},
		audit:   &rsAudit{},
		known:   known,
	}
	uc, err := NewPasswordResetUseCase(PasswordResetDeps{
		Users: f.users, Resets: f.resets, Tenants: f.tenants, Mailer: f.mailer, Queue: f.queue, Audit: f.audit,
		Policies: authPolicies{}, Logger: zap.NewNop(), Now: func() time.Time { return resetNow }, PublicBaseURL: publicBaseURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.uc = uc
	return f
}

// untouched dice si la solicitud llego a algo que depende de la cuenta.
func (f *resetFixture) untouched() bool {
	return f.users.lookups == 0 && f.tenants.lookups == 0 && len(f.resets.created) == 0 &&
		f.resets.invalidated == 0 && len(f.mailer.sent) == 0 && len(f.audit.entries) == 0
}

// Pedir el reinicio solo encola, con un correo registrado y con uno desconocido: ninguna busqueda,
// ningun enlace y ningun envio antes de responder. Las dos solicitudes quedan igual en la cola.
func TestPedirElReinicioSoloEncola(t *testing.T) {
	f := newResetFixture(t, resetBaseURL)
	f.uc.RequestReset(context.Background(), "  Ana@Example.test ", "203.0.113.7")
	f.uc.RequestReset(context.Background(), "nadie@example.test", "203.0.113.7")
	want := []ports.PasswordResetRequest{
		{Email: "ana@example.test", IPAddress: "203.0.113.7"},
		{Email: "nadie@example.test", IPAddress: "203.0.113.7"},
	}
	if len(f.queue.got) != 2 || f.queue.got[0] != want[0] || f.queue.got[1] != want[1] {
		t.Fatalf("encolado %+v, se esperaba %+v", f.queue.got, want)
	}
	if !f.untouched() {
		t.Fatal("la solicitud busco la cuenta o genero un enlace antes de responder")
	}
}

// Lo que impide encolar no depende del correo: sin enlace que construir, sin a donde enviarlo o
// con la cola llena, ni la cuenta registrada ni la desconocida llegan a nada.
func TestSinConfiguracionOConLaColaLlenaNoSeHaceNada(t *testing.T) {
	cases := map[string]func(*resetFixture){
		"sin PUBLIC_BASE_URL":          func(*resetFixture) {},
		"sin correo transaccional":     func(f *resetFixture) { f.mailer.configured = false },
		"con la cola llena":            func(f *resetFixture) { f.queue.full = true },
		"con espacios en la URL vacia": func(*resetFixture) {},
	}
	for name, setup := range cases {
		base := resetBaseURL
		if name == "sin PUBLIC_BASE_URL" {
			base = ""
		} else if name == "con espacios en la URL vacia" {
			base = "   "
		}
		f := newResetFixture(t, base)
		setup(f)
		for _, email := range []string{f.known.Email, "nadie@example.test"} {
			f.uc.RequestReset(context.Background(), email, "203.0.113.7")
		}
		if len(f.queue.got) != 0 || !f.untouched() {
			t.Errorf("%s: se encolaron %d solicitudes o se toco la cuenta", name, len(f.queue.got))
		}
	}
}

var resetLink = regexp.MustCompile(`href="` + regexp.QuoteMeta(resetBaseURL) + `/reset-password\?token=([0-9a-f]+)"`)

// El trabajador genera el enlace de una cuenta que puede recuperarse: un solo enlace vigente,
// solo su resumen guardado, caducidad a los 30 minutos del reloj del caso y el correo al titular.
func TestAtenderLaSolicitudDeUnaCuenta(t *testing.T) {
	f := newResetFixture(t, resetBaseURL)
	f.uc.ProcessReset(context.Background(), ports.PasswordResetRequest{Email: f.known.Email, IPAddress: "203.0.113.7"})

	if f.resets.invalidated != 1 || len(f.resets.created) != 1 || len(f.mailer.sent) != 1 {
		t.Fatalf("invalidados %d, creados %d, enviados %d", f.resets.invalidated, len(f.resets.created), len(f.mailer.sent))
	}
	mail := f.mailer.sent[0]
	m := resetLink.FindStringSubmatch(mail.body)
	if m == nil || mail.to != f.known.Email || mail.tenant != f.known.TenantID {
		t.Fatalf("correo %+v sin el enlace esperado", mail)
	}
	token, err := url.QueryUnescape(m[1])
	if err != nil {
		t.Fatal(err)
	}
	tok := f.resets.created[0]
	if tok.TokenHash != hashResetToken(token) || tok.TokenHash == token {
		t.Error("se guardo otra cosa que el resumen del token")
	}
	if !tok.CreatedAt.Equal(resetNow) || !tok.ExpiresAt.Equal(resetNow.Add(resetTokenTTL)) || tok.UserID != f.known.ID {
		t.Errorf("token %+v", tok)
	}
	if len(f.audit.entries) != 1 || f.audit.entries[0].IPAddress != "203.0.113.7" || f.audit.entries[0].Action != "password_reset_requested" {
		t.Errorf("bitacora %+v", f.audit.entries)
	}
}

// La solicitud es publica y sin sesion: quien conozca un correo podria llenarle el buzon de enlaces y,
// como cada enlace nuevo anula el anterior, impedirle terminar nunca un reinicio legitimo. Un correo
// que ya tuvo un enlace hace poco no recibe otro ni anula el vigente, y un fallo al comprobarlo
// tampoco envia nada.
func TestUnaCuentaConUnEnlaceRecienteNoRecibeOtro(t *testing.T) {
	for name, setup := range map[string]func(*resetFixture){
		"enlace reciente":        func(f *resetFixture) { f.resets.recent = true },
		"no se pudo comprobarlo": func(f *resetFixture) { f.resets.recentErr = errors.New("base caida") },
	} {
		f := newResetFixture(t, resetBaseURL)
		setup(f)
		f.uc.ProcessReset(context.Background(), ports.PasswordResetRequest{Email: f.known.Email, IPAddress: "203.0.113.7"})
		if f.resets.invalidated != 0 || len(f.resets.created) != 0 || len(f.mailer.sent) != 0 || len(f.audit.entries) != 0 {
			t.Errorf("%s: invalidados %d, creados %d, enviados %d", name, f.resets.invalidated, len(f.resets.created), len(f.mailer.sent))
		}
		if want := resetNow.Add(-resetCooldown); !f.resets.since.Equal(want) {
			t.Errorf("%s: se pregunto desde %v, se esperaba %v", name, f.resets.since, want)
		}
	}
}

// Un correo desconocido o de una cuenta inactiva no genera enlace ni correo.
func TestAtenderLaSolicitudSinCuentaQueRecuperar(t *testing.T) {
	for _, email := range []string{"nadie@example.test", "baja@example.test"} {
		f := newResetFixture(t, resetBaseURL)
		f.uc.ProcessReset(context.Background(), ports.PasswordResetRequest{Email: email})
		if len(f.resets.created) != 0 || f.resets.invalidated != 0 || len(f.mailer.sent) != 0 || len(f.audit.entries) != 0 {
			t.Errorf("%s: se genero un enlace o un correo", email)
		}
	}
}

// La caducidad del enlace se decide con el reloj del caso de uso.
func TestElEnlaceCaducaConElRelojDelCaso(t *testing.T) {
	f := newResetFixture(t, resetBaseURL)
	for name, expires := range map[string]time.Time{"vencido": resetNow.Add(-time.Second), "vence ahora": resetNow} {
		f.resets.byHash[hashResetToken(name)] = &domain.PasswordResetToken{ID: uuid.New(), UserID: f.known.ID, ExpiresAt: expires}
	}
	if err := f.uc.ConfirmReset(context.Background(), "vencido", "Nueva-Clave-2030!"); !errors.Is(err, domain.ErrResetTokenInvalid) {
		t.Errorf("enlace vencido: %v", err)
	}
	if _, err := f.uc.RulesForToken(context.Background(), "vencido"); !errors.Is(err, domain.ErrResetTokenInvalid) {
		t.Errorf("reglas de un enlace vencido: %v", err)
	}
	if _, err := f.uc.RulesForToken(context.Background(), "vence ahora"); err != nil {
		t.Errorf("reglas de un enlace que vence ahora: %v", err)
	}
}

func TestSinColaNoHayCasoDeUso(t *testing.T) {
	if _, err := NewPasswordResetUseCase(PasswordResetDeps{Mailer: &rsMailer{}}); err == nil {
		t.Fatal("sin cola el caso de uso no debe construirse")
	}
	if _, err := NewPasswordResetUseCase(PasswordResetDeps{Queue: &rsQueue{}}); err == nil {
		t.Fatal("sin cliente de correo el caso de uso no debe construirse")
	}
}
