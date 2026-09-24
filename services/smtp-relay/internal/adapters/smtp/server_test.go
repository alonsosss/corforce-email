package smtp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"
	"go.uber.org/zap"

	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/adapters/mime"
	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/app"
	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/domain"
)

const (
	prefix   = "abcdefgh2345"
	password = "cfm_" + prefix + "_secreto"
	eicar    = `X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`
)

type stubAuth struct {
	mu      sync.Mutex
	revoked bool
}

func (a *stubAuth) Authenticate(_ context.Context, token, _ string) (*domain.Credential, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if token != password || a.revoked {
		return nil, domain.ErrAuthFailed
	}
	return &domain.Credential{KeyID: "k1", TenantID: "t1", Prefix: prefix, Token: token}, nil
}

type stubThrottle struct{}

func (stubThrottle) Blocked(context.Context, string, string) bool { return false }
func (stubThrottle) Failure(context.Context, string, string)      {}
func (stubThrottle) Success(context.Context, string, string)      {}

type stubLimits struct{ connections bool }

func (l stubLimits) AllowConnection(context.Context, string) bool    { return l.connections }
func (stubLimits) AllowMessage(context.Context, string, string) bool { return true }

type stubScanner struct{}

func (stubScanner) Scan(_ context.Context, raw []byte) error {
	if strings.Contains(string(raw), "EICAR-STANDARD-ANTIVIRUS-TEST-FILE") {
		return domain.ErrInfected
	}
	return nil
}

type stubSubmitter struct {
	mu   sync.Mutex
	envs []domain.Envelope
	err  error
}

func (s *stubSubmitter) Submit(_ context.Context, _ domain.Credential, env domain.Envelope, _ []byte, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	s.envs = append(s.envs, env)
	return "m1", nil
}

type nopMetrics struct{}

func (nopMetrics) Connection(string)            {}
func (nopMetrics) Auth(string)                  {}
func (nopMetrics) Rejected(string, string)      {}
func (nopMetrics) Delivered(int, time.Duration) {}

func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "smtp.relay.test"},
		DNSNames: []string{"smtp.relay.test"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

type harness struct {
	auth      *stubAuth
	submitter *stubSubmitter
	starttls  string
	implicit  string
}

func start(t *testing.T, limits stubLimits, maxConnections int) *harness {
	t.Helper()
	h := &harness{auth: &stubAuth{}, submitter: &stubSubmitter{}}
	relay := app.New(app.Deps{
		Auth: h.auth, Throttle: stubThrottle{}, Limits: limits, Inspector: mime.New(64 << 10), Scanner: stubScanner{},
		Submitter: h.submitter, Metrics: nopMetrics{}, Config: app.Config{MaxRecipients: 2, MaxMessageBytes: 64 << 10},
	})
	cert := selfSigned(t)
	srv := NewServer(relay, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, Options{
		Domain: "smtp.relay.test", MaxMessageBytes: 64 << 10, MaxRecipients: 2, MaxConnections: maxConnections,
		MaxConcurrentData: 2, DataWait: time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second,
		DeliverTimeout: 5 * time.Second,
	}, zap.NewNop())
	l1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.ServeStartTLS(l1) }()
	go func() { _ = srv.ServeTLS(l2) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	})
	h.starttls, h.implicit = l1.Addr().String(), l2.Addr().String()
	return h
}

var clientTLS = &tls.Config{InsecureSkipVerify: true, ServerName: "smtp.relay.test"} //nolint:gosec // certificado de la prueba

func dial(t *testing.T, addr string) *gosmtp.Client {
	t.Helper()
	c, err := gosmtp.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Hello("cliente.test"); err != nil {
		t.Fatal(err)
	}
	return c
}

func dialStartTLS(t *testing.T, addr string) *gosmtp.Client {
	t.Helper()
	c, err := gosmtp.DialStartTLS(addr, clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func code(err error) int {
	var e *gosmtp.SMTPError
	if errors.As(err, &e) {
		return e.Code
	}
	return 0
}

func send(c *gosmtp.Client, from string, to []string, body string) error {
	if err := c.Mail(from, nil); err != nil {
		return err
	}
	for _, r := range to {
		if err := c.Rcpt(r, nil); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(body)); err != nil {
		return err
	}
	return w.Close()
}

const plainMessage = "From: Envios <envios@empresa.test>\r\nTo: ana@destino.test\r\nSubject: Hola\r\n\r\nHola Ana\r\n"

func TestAuthSinTLSSeRechaza(t *testing.T) {
	h := start(t, stubLimits{connections: true}, 10)
	c := dial(t, h.starttls)
	if ok, _ := c.Extension("AUTH"); ok {
		t.Fatal("AUTH no se anuncia antes de STARTTLS")
	}
	if ok, _ := c.Extension("STARTTLS"); !ok {
		t.Fatal("STARTTLS debe anunciarse")
	}
	if err := c.Auth(sasl.NewPlainClient("", prefix, password)); err == nil {
		t.Fatal("AUTH en claro no puede aceptarse")
	}
	if err := c.Mail("envios@empresa.test", nil); code(err) != 530 {
		t.Fatalf("MAIL sin AUTH: %v", err)
	}
}

func TestStartTLSYAuthPlain(t *testing.T) {
	h := start(t, stubLimits{connections: true}, 10)
	c := dialStartTLS(t, h.starttls)
	if err := c.Auth(sasl.NewPlainClient("", prefix, "cfm_"+prefix+"_malo")); code(err) != 535 {
		t.Fatalf("credencial mala: %v", err)
	}
	if err := c.Auth(sasl.NewPlainClient("", prefix, password)); err != nil {
		t.Fatalf("AUTH PLAIN: %v", err)
	}
	if err := send(c, "envios@empresa.test", []string{"ana@destino.test"}, plainMessage); err != nil {
		t.Fatalf("envio: %v", err)
	}
	if len(h.submitter.envs) != 1 || h.submitter.envs[0].Recipients[0] != "ana@destino.test" {
		t.Fatalf("llega a transactional: %+v", h.submitter.envs)
	}
}

func TestTLSImplicitoYAuthLogin(t *testing.T) {
	h := start(t, stubLimits{connections: true}, 10)
	conn, err := tls.Dial("tcp", h.implicit, clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	c := gosmtp.NewClient(conn)
	defer c.Close()
	if err := c.Hello("cliente.test"); err != nil {
		t.Fatal(err)
	}
	if ok, mechs := c.Extension("AUTH"); !ok || !strings.Contains(mechs, "LOGIN") || !strings.Contains(mechs, "PLAIN") {
		t.Fatalf("AUTH dentro de TLS: %v %q", ok, mechs)
	}
	if err := c.Auth(sasl.NewLoginClient(prefix, password)); err != nil {
		t.Fatalf("AUTH LOGIN: %v", err)
	}
	if err := send(c, "envios@empresa.test", []string{"ana@destino.test"}, plainMessage); err != nil {
		t.Fatalf("envio: %v", err)
	}
}

func authed(t *testing.T, h *harness) *gosmtp.Client {
	t.Helper()
	c := dialStartTLS(t, h.starttls)
	if err := c.Auth(sasl.NewPlainClient("", prefix, password)); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCodigosDeRechazo(t *testing.T) {
	h := start(t, stubLimits{connections: true}, 10)

	c := authed(t, h)
	if err := send(c, "envios@empresa.test", []string{"a@x.test", "b@x.test", "c@x.test"}, plainMessage); code(err) != 452 {
		t.Fatalf("demasiados destinatarios: %v", err)
	}
	_ = c.Reset()
	attachment := "From: envios@empresa.test\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"B\"\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\nadjunto\r\n--B\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"eicar.com\"\r\n\r\n" + eicar + "\r\n--B--\r\n"
	if err := send(c, "envios@empresa.test", []string{"a@x.test"}, attachment); code(err) != 554 {
		t.Fatalf("EICAR: %v", err)
	}
	if err := send(c, "envios@empresa.test", []string{"a@x.test"}, "Sin cabecera From\r\n\r\ncuerpo\r\n"); code(err) != 554 {
		t.Fatalf("MIME sin From: %v", err)
	}
	if err := send(c, "envios@empresa.test", []string{"a@x.test"}, plainMessage+strings.Repeat(strings.Repeat("x", 900)+"\r\n", 80)); code(err) != 552 {
		t.Fatalf("tamano: %v", err)
	}

	h.submitter.err = domain.ErrSenderNotVerified
	if err := send(c, "envios@ajeno.test", []string{"a@x.test"}, plainMessage); code(err) != 550 {
		t.Fatalf("remitente no verificado: %v", err)
	}
	h.submitter.err = domain.ErrUpstream
	if err := send(c, "envios@empresa.test", []string{"a@x.test"}, plainMessage); code(err) != 451 {
		t.Fatalf("transactional caido: %v", err)
	}

	h.auth.mu.Lock()
	h.auth.revoked = true
	h.auth.mu.Unlock()
	if err := c.Mail("envios@empresa.test", nil); code(err) != 535 {
		t.Fatalf("clave revocada durante la sesion: %v", err)
	}
}

func TestCupoDeConexiones(t *testing.T) {
	h := start(t, stubLimits{connections: false}, 10)
	conn, err := net.Dial("tcp", h.starttls)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 128)
	n, _ := conn.Read(buf)
	if !strings.HasPrefix(string(buf[:n]), "421 4.7.0") {
		t.Fatalf("una IP sin cupo recibe 421 y se cierra: %q", buf[:n])
	}
}

func TestTopeDeConexionesAbiertas(t *testing.T) {
	h := start(t, stubLimits{connections: true}, 1)
	first := dial(t, h.starttls)
	conn, err := net.Dial("tcp", h.starttls)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 128)
	n, _ := conn.Read(buf)
	if !strings.HasPrefix(string(buf[:n]), "421") {
		t.Fatalf("sin hueco: %q", buf[:n])
	}
	_ = first.Quit()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := gosmtp.Dial(h.starttls)
		if err == nil {
			hello := c.Hello("cliente.test")
			_ = c.Close()
			if hello == nil {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("al cerrarse la primera conexion su hueco queda libre")
}

func TestRepliesCubrenTodosLosRechazos(t *testing.T) {
	all := []*domain.Rejection{
		domain.ErrAuthRequired, domain.ErrAuthFailed, domain.ErrAuthUnavailable, domain.ErrBlocked, domain.ErrConnectionLimit,
		domain.ErrMessageRate, domain.ErrTooManyRecipients, domain.ErrInvalidAddress, domain.ErrTooLarge, domain.ErrMalformed,
		domain.ErrInfected, domain.ErrScanUnavailable, domain.ErrKeyRevoked, domain.ErrSenderNotVerified, domain.ErrSendingDenied,
		domain.ErrSendingThrottled, domain.ErrRejected, domain.ErrUpstream,
	}
	if len(replies) != len(all) {
		t.Fatalf("respuestas: %d, rechazos: %d", len(replies), len(all))
	}
	for _, r := range all {
		rep, ok := reply(fmt.Errorf("envuelto: %w", r)).(*gosmtp.SMTPError)
		if !ok {
			t.Fatalf("%s sin respuesta", r.Reason)
		}
		temporary := rep.Code >= 400 && rep.Code < 500
		if temporary != (r.Kind == domain.Temporary) || rep.EnhancedCode[0] != rep.Code/100 {
			t.Errorf("%s: %d %v no casa con su clase", r.Reason, rep.Code, rep.EnhancedCode)
		}
		for _, leak := range []string{"transactional", "access-control", "redis", "clamd", "tenant"} {
			if strings.Contains(strings.ToLower(rep.Message), leak) {
				t.Errorf("%s filtra %q: %q", r.Reason, leak, rep.Message)
			}
		}
	}
	if rep := reply(errors.New("desconocido")).(*gosmtp.SMTPError); rep.Code != 451 {
		t.Errorf("un error desconocido es temporal: %d", rep.Code)
	}
}

func TestLoginServer(t *testing.T) {
	var gotUser, gotPass string
	s := newLoginServer(func(u, p string) error { gotUser, gotPass = u, p; return nil })
	if ch, done, _ := s.Next(nil); done || string(ch) != "Username:" {
		t.Fatalf("pide usuario: %q", ch)
	}
	if ch, done, _ := s.Next([]byte("usuario")); done || string(ch) != "Password:" {
		t.Fatalf("pide contrasena: %q", ch)
	}
	if _, done, err := s.Next([]byte("clave")); !done || err != nil || gotUser != "usuario" || gotPass != "clave" {
		t.Fatalf("autentica: %v %q %q", err, gotUser, gotPass)
	}
	if _, _, err := s.Next([]byte("mas")); err == nil {
		t.Fatal("despues de terminar no admite mas")
	}
	failing := newLoginServer(func(string, string) error { return domain.ErrAuthFailed })
	_, _, _ = failing.Next([]byte("usuario"))
	if _, done, err := failing.Next([]byte("mala")); !done || err == nil {
		t.Fatal("una credencial mala termina con error")
	}
}
