package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/domain"
)

const (
	prefix = "abcdefgh2345"
	token  = "cfm_" + prefix + "_secreto"
)

type fakeAuth struct {
	valid map[string]*domain.Credential
	err   error
	calls int
}

func (a *fakeAuth) Authenticate(_ context.Context, tok, _ string) (*domain.Credential, error) {
	a.calls++
	if a.err != nil {
		return nil, a.err
	}
	if c, ok := a.valid[tok]; ok {
		cp := *c
		return &cp, nil
	}
	return nil, domain.ErrAuthFailed
}

type fakeThrottle struct {
	blocked  bool
	failures []string
	success  []string
}

func (t *fakeThrottle) Blocked(context.Context, string, string) bool { return t.blocked }
func (t *fakeThrottle) Failure(_ context.Context, u, ip string) {
	t.failures = append(t.failures, u+"|"+ip)
}
func (t *fakeThrottle) Success(_ context.Context, u, ip string) {
	t.success = append(t.success, u+"|"+ip)
}

type fakeLimits struct{ connections, messages bool }

func (l *fakeLimits) AllowConnection(context.Context, string) bool      { return l.connections }
func (l *fakeLimits) AllowMessage(context.Context, string, string) bool { return l.messages }

type fakeInspector struct {
	inspection domain.Inspection
	err        error
}

func (i *fakeInspector) Inspect([]byte) (domain.Inspection, error) { return i.inspection, i.err }

// fakeScanner reconoce la firma de prueba EICAR como hace clamd.
type fakeScanner struct {
	err   error
	calls int
}

const eicar = `X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`

func (s *fakeScanner) Scan(_ context.Context, raw []byte) error {
	s.calls++
	if s.err != nil {
		return s.err
	}
	if strings.Contains(string(raw), "EICAR-STANDARD-ANTIVIRUS-TEST-FILE") {
		return fmt.Errorf("%w: Win.Test.EICAR_HDB-1", domain.ErrInfected)
	}
	return nil
}

type submission struct {
	cred domain.Credential
	env  domain.Envelope
	raw  []byte
	key  string
}

type fakeSubmitter struct {
	subs []submission
	err  error
}

func (s *fakeSubmitter) Submit(_ context.Context, cred domain.Credential, env domain.Envelope, raw []byte, key string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	s.subs = append(s.subs, submission{cred, env, raw, key})
	return "msg-1", nil
}

type fakeMetrics struct {
	rejected  []string
	auths     []string
	delivered int
}

func (m *fakeMetrics) Connection(string) {}
func (m *fakeMetrics) Auth(r string)     { m.auths = append(m.auths, r) }
func (m *fakeMetrics) Rejected(stage, reason string) {
	m.rejected = append(m.rejected, stage+"/"+reason)
}
func (m *fakeMetrics) Delivered(int, time.Duration) { m.delivered++ }

type fixture struct {
	relay     *Relay
	auth      *fakeAuth
	throttle  *fakeThrottle
	limits    *fakeLimits
	inspector *fakeInspector
	scanner   *fakeScanner
	submitter *fakeSubmitter
	metrics   *fakeMetrics
	cred      *domain.Credential
}

func newFixture(withScanner bool) *fixture {
	f := &fixture{
		cred:      &domain.Credential{KeyID: "k1", TenantID: "t1", Prefix: prefix, Token: token},
		throttle:  &fakeThrottle{},
		limits:    &fakeLimits{connections: true, messages: true},
		inspector: &fakeInspector{},
		scanner:   &fakeScanner{},
		submitter: &fakeSubmitter{},
		metrics:   &fakeMetrics{},
	}
	f.auth = &fakeAuth{valid: map[string]*domain.Credential{token: f.cred}}
	deps := Deps{
		Auth: f.auth, Throttle: f.throttle, Limits: f.limits, Inspector: f.inspector,
		Submitter: f.submitter, Metrics: f.metrics, Config: Config{MaxRecipients: 3, MaxMessageBytes: 1 << 10},
	}
	if withScanner {
		deps.Scanner = f.scanner
	}
	f.relay = New(deps)
	return f
}

var ctx = context.Background()

func TestAdmit(t *testing.T) {
	f := newFixture(true)
	if err := f.relay.Admit(ctx, "starttls", "198.51.100.7"); err != nil {
		t.Fatal(err)
	}
	f.limits.connections = false
	if err := f.relay.Admit(ctx, "starttls", "198.51.100.7"); !errors.Is(err, domain.ErrConnectionLimit) {
		t.Fatalf("cupo de conexiones: %v", err)
	}
}

func TestAuthenticate(t *testing.T) {
	f := newFixture(true)
	cred, err := f.relay.Authenticate(ctx, prefix, token, "198.51.100.7")
	if err != nil || cred.KeyID != "k1" || len(f.throttle.success) != 1 {
		t.Fatalf("credencial valida: %+v %v", cred, err)
	}

	// Un usuario que no es el prefijo de la contrasena no llega a access-control y cuenta como fallo.
	calls := f.auth.calls
	if _, err := f.relay.Authenticate(ctx, "otroprefijo2", token, "198.51.100.7"); !errors.Is(err, domain.ErrAuthFailed) {
		t.Fatalf("usuario distinto: %v", err)
	}
	if f.auth.calls != calls || len(f.throttle.failures) != 1 {
		t.Fatalf("sin consulta y con fallo anotado: %d %v", f.auth.calls, f.throttle.failures)
	}

	// Credencial revocada, caducada o sin permiso: access-control la rechaza y cuenta como fallo.
	if _, err := f.relay.Authenticate(ctx, prefix, "cfm_"+prefix+"_otro", "198.51.100.7"); !errors.Is(err, domain.ErrAuthFailed) {
		t.Fatalf("clave rechazada: %v", err)
	}
	if len(f.throttle.failures) != 2 {
		t.Fatal("el rechazo cuenta en el freno")
	}

	f.throttle.blocked = true
	if _, err := f.relay.Authenticate(ctx, prefix, token, "198.51.100.7"); !errors.Is(err, domain.ErrBlocked) {
		t.Fatalf("bloqueado: %v", err)
	}

	f = newFixture(true)
	f.auth.err = fmt.Errorf("%w: caido", domain.ErrAuthUnavailable)
	if _, err := f.relay.Authenticate(ctx, prefix, token, "198.51.100.7"); !errors.Is(err, domain.ErrAuthUnavailable) {
		t.Fatalf("access-control caido: %v", err)
	}
	if len(f.throttle.failures) != 0 {
		t.Fatal("una caida no cuenta como intento fallido")
	}
	if domain.AsRejection(domain.ErrAuthUnavailable).Kind != domain.Temporary {
		t.Fatal("una caida es temporal")
	}
}

func TestMail(t *testing.T) {
	f := newFixture(true)
	if _, err := f.relay.Mail(ctx, nil, "198.51.100.7", "a@empresa.test"); !errors.Is(err, domain.ErrAuthRequired) {
		t.Fatalf("sin AUTH: %v", err)
	}
	addr, err := f.relay.Mail(ctx, f.cred, "198.51.100.7", "Envios@Empresa.TEST")
	if err != nil || addr != "Envios@empresa.test" {
		t.Fatalf("remitente: %q %v", addr, err)
	}
	for _, bad := range []string{"", "sin-arroba", "a@b", "a b@c.test", "<>"} {
		if _, err := f.relay.Mail(ctx, f.cred, "198.51.100.7", bad); !errors.Is(err, domain.ErrInvalidAddress) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	f.limits.messages = false
	if _, err := f.relay.Mail(ctx, f.cred, "198.51.100.7", "a@empresa.test"); !errors.Is(err, domain.ErrMessageRate) {
		t.Fatalf("cupo de mensajes: %v", err)
	}
}

// Una clave revocada durante la sesion deja de enviar en el siguiente MAIL FROM.
func TestMailConClaveRevocadaEnLaSesion(t *testing.T) {
	f := newFixture(true)
	delete(f.auth.valid, token)
	if _, err := f.relay.Mail(ctx, f.cred, "198.51.100.7", "a@empresa.test"); !errors.Is(err, domain.ErrKeyRevoked) {
		t.Fatalf("revocada: %v", err)
	}
	f.auth.valid[token] = &domain.Credential{KeyID: "k1", TenantID: "otra", Token: token}
	if _, err := f.relay.Mail(ctx, f.cred, "198.51.100.7", "a@empresa.test"); !errors.Is(err, domain.ErrKeyRevoked) {
		t.Fatalf("otra empresa: %v", err)
	}
	f.auth.err = domain.ErrAuthUnavailable
	if _, err := f.relay.Mail(ctx, f.cred, "198.51.100.7", "a@empresa.test"); !errors.Is(err, domain.ErrAuthUnavailable) {
		t.Fatalf("sin respuesta: %v", err)
	}
}

func TestRcpt(t *testing.T) {
	f := newFixture(true)
	var got []string
	var err error
	for _, to := range []string{"a@x.test", "A@X.test", "b@x.test", "c@x.test"} {
		if got, err = f.relay.Rcpt(got, to); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 3 {
		t.Fatalf("repetido sin duplicar: %v", got)
	}
	if got, err = f.relay.Rcpt(got, "d@x.test"); !errors.Is(err, domain.ErrTooManyRecipients) || len(got) != 3 {
		t.Fatalf("tope de destinatarios: %v %v", got, err)
	}
	if domain.AsRejection(err).Kind != domain.Temporary {
		t.Fatal("el exceso de destinatarios es temporal: el cliente reintenta el resto en otro mensaje")
	}
	if _, err := f.relay.Rcpt(nil, "roto"); !errors.Is(err, domain.ErrInvalidAddress) {
		t.Fatalf("direccion rota: %v", err)
	}
}

func env() domain.Envelope {
	return domain.Envelope{From: "a@empresa.test", Recipients: []string{"b@x.test"}}
}

func TestDeliver(t *testing.T) {
	f := newFixture(true)
	f.inspector.inspection = domain.Inspection{MessageID: "id@empresa.test"}
	id, err := f.relay.Deliver(ctx, f.cred, env(), []byte("From: a@empresa.test\r\n\r\nhola"))
	if err != nil || id != "msg-1" || f.metrics.delivered != 1 {
		t.Fatalf("entrega: %q %v", id, err)
	}
	sub := f.submitter.subs[0]
	if sub.cred.TenantID != "t1" || sub.key == "" || !strings.HasPrefix(sub.key, "smtp-") || f.scanner.calls != 0 {
		t.Fatalf("envio sin adjuntos, con clave de idempotencia: %+v", sub)
	}
	if _, err := f.relay.Deliver(ctx, nil, env(), []byte("x")); !errors.Is(err, domain.ErrAuthRequired) {
		t.Fatalf("sin credencial: %v", err)
	}
	if _, err := f.relay.Deliver(ctx, f.cred, domain.Envelope{From: "a@empresa.test"}, []byte("x")); !errors.Is(err, domain.ErrInvalidAddress) {
		t.Fatalf("sin destinatarios: %v", err)
	}
}

func TestDeliverLimitesYMIME(t *testing.T) {
	f := newFixture(true)
	if _, err := f.relay.Deliver(ctx, f.cred, env(), make([]byte, 2<<10)); !errors.Is(err, domain.ErrTooLarge) {
		t.Fatalf("tamano: %v", err)
	}
	f.inspector.err = fmt.Errorf("%w: base64", domain.ErrMalformed)
	if _, err := f.relay.Deliver(ctx, f.cred, env(), []byte("x")); !errors.Is(err, domain.ErrMalformed) {
		t.Fatalf("MIME malformado: %v", err)
	}
	if domain.AsRejection(domain.ErrMalformed).Kind != domain.Permanent || len(f.submitter.subs) != 0 {
		t.Fatal("un MIME malformado es definitivo y no llega a transactional")
	}
}

func TestDeliverAdjuntos(t *testing.T) {
	f := newFixture(true)
	f.inspector.inspection = domain.Inspection{HasAttachments: true}
	raw := []byte("From: a@empresa.test\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: application/octet-stream\r\n\r\n" + eicar + "\r\n--b--\r\n")
	if _, err := f.relay.Deliver(ctx, f.cred, env(), []byte("limpio")); err != nil {
		t.Fatalf("un adjunto limpio sale: %v", err)
	}
	f.relay.d.Config.MaxMessageBytes = 1 << 20
	if _, err := f.relay.Deliver(ctx, f.cred, env(), raw); !errors.Is(err, domain.ErrInfected) {
		t.Fatalf("EICAR: %v", err)
	}
	if len(f.submitter.subs) != 1 || f.scanner.calls != 2 {
		t.Fatalf("el infectado no llega a transactional: %d envios, %d analisis", len(f.submitter.subs), f.scanner.calls)
	}
	f.scanner.err = fmt.Errorf("%w: sin clamd", domain.ErrScanUnavailable)
	if _, err := f.relay.Deliver(ctx, f.cred, env(), []byte("x")); !errors.Is(err, domain.ErrScanUnavailable) {
		t.Fatalf("clamd caido: %v", err)
	}

	f = newFixture(false)
	f.inspector.inspection = domain.Inspection{HasAttachments: true}
	if _, err := f.relay.Deliver(ctx, f.cred, env(), []byte("x")); !errors.Is(err, domain.ErrScanUnavailable) {
		t.Fatalf("sin analizador no sale ningun adjunto: %v", err)
	}
}

func TestDeliverRechazoDeTransactional(t *testing.T) {
	for _, want := range []*domain.Rejection{domain.ErrSenderNotVerified, domain.ErrSendingDenied, domain.ErrSendingThrottled, domain.ErrUpstream} {
		f := newFixture(true)
		f.submitter.err = want
		if _, err := f.relay.Deliver(ctx, f.cred, env(), []byte("x")); !errors.Is(err, want) {
			t.Errorf("%s: %v", want.Reason, err)
		}
		if f.metrics.rejected[len(f.metrics.rejected)-1] != StageData+"/"+want.Reason {
			t.Errorf("metrica: %v", f.metrics.rejected)
		}
	}
}
