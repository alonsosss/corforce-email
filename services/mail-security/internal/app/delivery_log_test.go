package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
)

// deliverySource es la lista de Redis en memoria: Lines espera, Work es la lista de trabajo.
type deliverySource struct {
	Lines    []string
	Work     []string
	Contexts map[string]domain.QueueContext
}

func (s *deliverySource) Next(context.Context, time.Duration) (string, bool, error) {
	if len(s.Lines) == 0 {
		return "", false, nil
	}
	raw := s.Lines[0]
	s.Lines = s.Lines[1:]
	s.Work = append(s.Work, raw)
	return raw, true, nil
}
func (s *deliverySource) Pending(context.Context) ([]string, error) {
	return append([]string(nil), s.Work...), nil
}
func (s *deliverySource) Ack(_ context.Context, raw string) error {
	for i, w := range s.Work {
		if w == raw {
			s.Work = append(s.Work[:i], s.Work[i+1:]...)
			break
		}
	}
	return nil
}
func (s *deliverySource) Backlog(context.Context) (int64, error) { return int64(len(s.Lines)), nil }
func (s *deliverySource) LoadContext(_ context.Context, qid string) (domain.QueueContext, error) {
	return s.Contexts[qid], nil
}
func (s *deliverySource) SaveContext(_ context.Context, qid string, qc domain.QueueContext, _ time.Duration) error {
	s.Contexts[qid] = qc
	return nil
}

type deliveryRepo struct {
	Events []domain.DeliveryEvent
	keys   map[string]bool
	Fail   error
}

func (r *deliveryRepo) Insert(_ context.Context, e *domain.DeliveryEvent) (bool, error) {
	if r.Fail != nil {
		return false, r.Fail
	}
	k := e.TenantID.String() + e.EventKey
	if r.keys[k] {
		return false, nil
	}
	r.keys[k] = true
	r.Events = append(r.Events, *e)
	return true, nil
}
func (r *deliveryRepo) List(_ context.Context, tenantID uuid.UUID, f domain.DeliveryFilter) ([]domain.DeliveryEvent, int64, error) {
	var out []domain.DeliveryEvent
	for _, e := range r.Events {
		if e.TenantID == tenantID {
			out = append(out, e)
		}
	}
	return out, int64(len(out)), nil
}
func (r *deliveryRepo) PruneBefore(context.Context, time.Time, int) (int64, error) { return 0, nil }

type deliveryMetrics struct{ Outcomes map[string]int }

func (m *deliveryMetrics) DeliveryLineProcessed(o string) { m.Outcomes[o]++ }
func (m *deliveryMetrics) DeliveryBacklog(int64)          {}
func (m *deliveryMetrics) DeliveryStoreFailed()           { m.Outcomes["store_failed"]++ }

type deliveryHarness struct {
	log     *DeliveryLog
	source  *deliverySource
	repo    *deliveryRepo
	metrics *deliveryMetrics
	campo   uuid.UUID
	mentor  uuid.UUID
}

func newDeliveryHarness() *deliveryHarness {
	h := &deliveryHarness{
		source:  &deliverySource{Contexts: map[string]domain.QueueContext{}},
		repo:    &deliveryRepo{keys: map[string]bool{}},
		metrics: &deliveryMetrics{Outcomes: map[string]int{}},
		campo:   uuid.New(), mentor: uuid.New(),
	}
	dir := apptest.NewDirectory()
	dir.Domains["campovivoalimentos.com"] = h.campo
	dir.Domains["mentorenergy.uk"] = h.mentor
	dir.AliasDomains["campovivo.pe"] = "campovivoalimentos.com"
	dir.Mailboxes["atencion@campovivoalimentos.com"] = domain.Mailbox{TenantID: h.campo, Username: "atencion@campovivoalimentos.com", Domain: "campovivoalimentos.com", Active: 1}
	h.log = NewDeliveryLog(DeliveryLogDeps{Source: h.source, Repo: h.repo, Tx: &apptest.Tx{}, Directory: dir, Metrics: h.metrics, Retention: time.Hour})
	return h
}

func pline(program, message string) string {
	return `{"time":"1790466052","program":"` + program + `","priority":"info","message":"` +
		strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(message) + `"}`
}

func (h *deliveryHarness) feed(t *testing.T, lines ...string) {
	t.Helper()
	h.source.Lines = append(h.source.Lines, lines...)
	for len(h.source.Lines) > 0 || len(h.source.Work) > 0 {
		if err := h.log.step(context.Background()); err != nil {
			t.Fatalf("step: %v", err)
		}
	}
}

func (h *deliveryHarness) events(tenant uuid.UUID) []domain.DeliveryEvent {
	out, _, _ := h.repo.List(context.Background(), tenant, domain.DeliveryFilter{})
	return out
}

func TestUnEnvioAutenticadoSeRegistraEnLaEmpresaDelBuzon(t *testing.T) {
	h := newDeliveryHarness()
	h.feed(t,
		pline("postfix/submission/smtpd", "C9E8C60485: client=ec2[23.22.171.91], sasl_method=PLAIN, sasl_username=atencion@campovivoalimentos.com"),
		pline("postfix/cleanup", "C9E8C60485: message-id=<m1@campovivoalimentos.com>"),
		pline("postfix/qmgr", "C9E8C60485: from=<atencion@campovivoalimentos.com>, size=1957, nrcpt=2 (queue active)"),
		pline("postfix/smtp", "C9E8C60485: to=<ana@gmail.com>, relay=gmail-smtp-in.l.google.com[142.251.127.27]:25, delay=2, delays=1.3/0.03/0.11/0.53, dsn=2.0.0, status=sent (250 2.0.0 OK)"),
		pline("postfix/smtp", "C9E8C60485: to=<nadie@empresa.example>, relay=mx.empresa.example[203.0.113.5]:25, delay=1.2, delays=0.1/0/0.5/0.6, dsn=5.1.1, status=bounced (host mx.empresa.example[203.0.113.5] said: 550 5.1.1 User unknown)"),
	)
	evs := h.events(h.campo)
	if len(evs) != 2 {
		t.Fatalf("una fila por destinatario: %+v", evs)
	}
	sent, bounced := evs[0], evs[1]
	if sent.Direction != domain.DirectionOutbound || sent.Status != domain.DeliverySent || sent.Sender != "atencion@campovivoalimentos.com" ||
		sent.MessageID != "m1@campovivoalimentos.com" || sent.SASLUsername != "atencion@campovivoalimentos.com" || sent.ClientIP != "23.22.171.91" {
		t.Errorf("enviado: %+v", sent)
	}
	if bounced.Status != domain.DeliveryBounced || bounced.DSN != "5.1.1" || !strings.Contains(bounced.Reason, "User unknown") {
		t.Errorf("rebote: %+v", bounced)
	}
	if len(h.events(h.mentor)) != 0 || len(h.source.Work) != 0 {
		t.Error("nada en otra empresa y la lista de trabajo queda vacia")
	}
}

func TestUnRemitenteFalsificadoNoSeAtribuyeALaEmpresa(t *testing.T) {
	h := newDeliveryHarness()
	h.feed(t,
		pline("postfix/smtpd", "AB12CD34EF: client=unknown[198.51.100.9]"),
		pline("postfix/qmgr", "AB12CD34EF: from=<atencion@campovivoalimentos.com>, size=100, nrcpt=1 (queue active)"),
		pline("postfix/lmtp", "AB12CD34EF: to=<ventas@mentorenergy.uk>, relay=dovecot[172.22.1.250]:24, delay=0.5, delays=0.1/0.1/0.1/0.2, dsn=2.0.0, status=sent (250 2.0.0 Saved)"),
	)
	if len(h.events(h.campo)) != 0 {
		t.Error("sin usuario autenticado, el remitente del sobre no basta para atribuir un envio")
	}
	in := h.events(h.mentor)
	if len(in) != 1 || in[0].Direction != domain.DirectionInbound || in[0].Sender != "atencion@campovivoalimentos.com" {
		t.Errorf("entrante en la empresa del destinatario: %+v", in)
	}
}

func TestUnCorreoEntreDosEmpresasQuedaEnLasDos(t *testing.T) {
	h := newDeliveryHarness()
	h.feed(t,
		pline("postfix/submission/smtpd", "AB12CD34EF: client=x[10.0.0.1], sasl_method=PLAIN, sasl_username=atencion@campovivoalimentos.com"),
		pline("postfix/lmtp", "AB12CD34EF: to=<ventas@mentorenergy.uk>, relay=dovecot[172.22.1.250]:24, delay=0.5, delays=0.1/0.1/0.1/0.2, dsn=2.0.0, status=sent (250 2.0.0 Saved)"),
	)
	if out, in := h.events(h.campo), h.events(h.mentor); len(out) != 1 || out[0].Direction != domain.DirectionOutbound ||
		len(in) != 1 || in[0].Direction != domain.DirectionInbound {
		t.Errorf("saliente en una y entrante en la otra: %+v %+v", out, in)
	}
}

func TestRechazosALaEntradaYDominioAlias(t *testing.T) {
	h := newDeliveryHarness()
	h.feed(t,
		pline("postfix/postscreen", "NOQUEUE: reject: RCPT from [198.51.100.9]:40000: 550 5.7.1 Service unavailable; client [198.51.100.9] blocked using zen.spamhaus.org; from=<a@b.example>, to=<ventas@campovivo.pe>, proto=ESMTP, helo=<b>"),
		pline("postfix/cleanup", "AB12CD34EF: milter-reject: END-OF-MESSAGE from unknown[198.51.100.9]: 5.7.1 Spam message rejected; from=<spam@malo.example> to=<atencion@campovivoalimentos.com> proto=ESMTP helo=<malo>"),
		pline("postfix/postscreen", "NOQUEUE: reject: RCPT from [198.51.100.9]:40000: 550 5.7.1 Service unavailable; from=<a@b.example>, to=<x@otra.example>, proto=ESMTP, helo=<b>"),
	)
	evs := h.events(h.campo)
	if len(evs) != 2 || evs[0].Status != domain.DeliveryRejected || evs[0].Direction != domain.DirectionInbound ||
		!strings.Contains(evs[0].Reason, "spamhaus") || !strings.Contains(evs[1].Reason, "Spam message rejected") {
		t.Fatalf("rechazos: %+v", evs)
	}
	if h.metrics.Outcomes["unowned"] != 1 {
		t.Errorf("el rechazo a un dominio ajeno no es de nadie: %v", h.metrics.Outcomes)
	}
}

func TestLaMismaLineaDosVecesNoDuplicaYUnFalloDeLaBaseReintenta(t *testing.T) {
	h := newDeliveryHarness()
	auth := pline("postfix/submission/smtpd", "AB12CD34EF: client=x[10.0.0.1], sasl_method=PLAIN, sasl_username=atencion@campovivoalimentos.com")
	sent := pline("postfix/smtp", "AB12CD34EF: to=<ana@gmail.com>, relay=g[1.2.3.4]:25, delay=1, delays=1/0/0/0, dsn=2.0.0, status=sent (250 OK)")
	h.feed(t, auth, sent, sent)
	if len(h.events(h.campo)) != 1 || h.metrics.Outcomes["duplicate"] != 1 {
		t.Fatalf("idempotente: %d filas, %v", len(h.events(h.campo)), h.metrics.Outcomes)
	}

	h.repo.Fail = errors.New("base caida")
	deferred := pline("postfix/smtp", "AB12CD34EF: to=<eva@gmail.com>, relay=none, delay=30, delays=0/0/30/0, dsn=4.4.1, status=deferred (connect timed out)")
	h.source.Lines = append(h.source.Lines, deferred)
	if err := h.log.step(context.Background()); err == nil {
		t.Fatal("un fallo de la base se devuelve para reintentar")
	}
	if len(h.source.Work) != 1 || h.metrics.Outcomes["store_failed"] != 1 {
		t.Fatalf("la linea se queda en la lista de trabajo: %v", h.source.Work)
	}
	h.repo.Fail = nil
	if err := h.log.step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if evs := h.events(h.campo); len(evs) != 2 || evs[1].Status != domain.DeliveryDeferred || len(h.source.Work) != 0 {
		t.Fatalf("al volver la base se guarda lo pendiente: %+v", evs)
	}
}

func TestListValidaLosFiltros(t *testing.T) {
	h := newDeliveryHarness()
	ctx := context.Background()
	from := time.Now()
	to := from.Add(-time.Hour)
	for name, f := range map[string]domain.DeliveryFilter{
		"direccion": {Direction: "lateral"},
		"estado":    {Status: "perdido"},
		"fechas":    {DateFrom: &from, DateTo: &to},
	} {
		var verr *domain.ValidationError
		if _, _, err := h.log.List(ctx, h.campo, f); !errors.As(err, &verr) {
			t.Errorf("%s: esperaba error de validacion, obtuve %v", name, err)
		}
	}
}

// Un dominio en convivencia: lo que no tiene buzon aqui se reenvia al proveedor anterior por un
// transporte. Ese correo es entrante para la empresa aunque no se entregue en la celda, y su rebote
// (el proveedor anterior ya no acepta la direccion) tiene que verse.
func TestElCorreoReenviadoAlProveedorAnteriorEsEntrante(t *testing.T) {
	h := newDeliveryHarness()
	h.feed(t,
		pline("postfix/smtpd", "AB12CD34EF: client=mail-oi1[209.85.1.1]"),
		pline("postfix/qmgr", "AB12CD34EF: from=<cliente@gmail.com>, size=100, nrcpt=2 (queue active)"),
		pline("postfix/smtp", "AB12CD34EF: to=<compras@campovivoalimentos.com>, relay=mx1.hostinger.com[172.65.182.103]:25, delay=1, delays=0.1/0/0.4/0.5, dsn=2.0.0, status=sent (250 2.0.0 Ok: queued as 4ABC)"),
		pline("postfix/smtp", "AB12CD34EF: to=<cf.prueba@campovivoalimentos.com>, relay=mx1.hostinger.com[172.65.182.103]:25, delay=1, delays=0.1/0/0.4/0.5, dsn=5.1.1, status=bounced (host mx1.hostinger.com[172.65.182.103] said: 550 5.1.1 <cf.prueba@campovivoalimentos.com>: Recipient address rejected: User unknown in virtual mailbox table (in reply to RCPT TO command))"),
		pline("postfix/smtp", "AB12CD34EF: to=<alguien@otra.example>, relay=mx.otra.example[203.0.113.9]:25, delay=1, delays=0.1/0/0.4/0.5, dsn=2.0.0, status=sent (250 ok)"),
	)
	evs := h.events(h.campo)
	if len(evs) != 2 || evs[0].Direction != domain.DirectionInbound || evs[0].Status != domain.DeliverySent ||
		evs[1].Status != domain.DeliveryBounced || !strings.Contains(evs[1].Reason, "User unknown") {
		t.Fatalf("entrante reenviado y su rebote: %+v", evs)
	}
	if h.metrics.Outcomes["unowned"] != 1 {
		t.Errorf("el envio a un dominio ajeno sin usuario autenticado no es de nadie: %v", h.metrics.Outcomes)
	}
}
