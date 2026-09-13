package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

const (
	testLinkKey = "clave-de-firma-de-pruebas-con-mas-de-32-caracteres"
	testCell    = "pe-01"
)

const testNoticeTemplate = `<p>{{.Count}} mensajes retenidos para {{.Mailbox}}</p>
<ul>{{range .Messages}}<li>{{.Subject}} de {{.Sender}} ({{.Score}}, {{.Date}})
<a href="{{.ReleaseURL}}">Liberar</a> <a href="{{.DiscardURL}}">Descartar</a></li>{{end}}</ul>`

type notifierFixture struct {
	q       *apptest.Quarantine
	notices *apptest.Notices
	tx      *apptest.Tx
	policy  *apptest.PolicyReader
	dir     *apptest.Directory
	sender  *apptest.NoticeSender
	links   *domain.QuarantineLinkSigner
	n       *QuarantineNotifier
	tenant  uuid.UUID
	now     time.Time
}

func newNotifierFixture(t *testing.T) *notifierFixture {
	t.Helper()
	links, err := domain.NewQuarantineLinkSigner(testLinkKey, "https://app.example.com", testCell, 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	q := &apptest.Quarantine{}
	f := &notifierFixture{
		q: q, notices: apptest.NewNotices(q), policy: apptest.NewPolicyReader(), dir: apptest.NewDirectory(),
		sender: &apptest.NoticeSender{}, links: links, tenant: uuid.New(),
		now: time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC),
	}
	f.tx = &apptest.Tx{Snapshot: f.notices.Snapshot}
	s := domain.DefaultQuarantineSettings(f.tenant)
	s.Notify = domain.QuarantineNotify{Enabled: true, MaxScore: decimal.NewFromInt(15), Sender: "cuarentena@acme.com",
		Subject: "Correo retenido en cuarentena", HTMLTemplate: testNoticeTemplate}
	f.policy.QSettings[f.tenant] = s
	for _, u := range []string{"ana@acme.com", "luis@acme.com"} {
		f.dir.Mailboxes[u] = domain.Mailbox{TenantID: f.tenant, Username: u, Domain: "acme.com", Active: 1}
	}
	f.n = NewQuarantineNotifier(NotifierDeps{Tx: f.tx, Policy: f.policy, Notices: f.notices, Directory: f.dir,
		Sender: f.sender, Links: links, Interval: time.Minute, Logger: zap.NewNop()})
	f.n.now = func() time.Time { return f.now }
	return f
}

func (f *notifierFixture) add(rcpt string, score int64, age time.Duration, subject, sender string) domain.QuarantineItem {
	id := uuid.New()
	sum := sha256.Sum256([]byte(id.String()))
	it := domain.QuarantineItem{ID: id, TenantID: f.tenant, Rcpt: rcpt, Subject: subject, Sender: sender,
		Score: decimal.NewFromInt(score), CreatedAt: f.now.Add(-age), QHash: hex.EncodeToString(sum[:]), Msg: []byte("m")}
	f.q.Items = append(f.q.Items, it)
	return it
}

func (f *notifierFixture) item(id uuid.UUID) domain.QuarantineItem {
	for _, it := range f.q.Items {
		if it.ID == id {
			return it
		}
	}
	return domain.QuarantineItem{}
}

func (f *notifierFixture) sweep(t *testing.T) SweepResult {
	t.Helper()
	res, err := f.n.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

var hrefRE = regexp.MustCompile(`href="([^"]+)"`)

// linkRequests saca del HTML del aviso los enlaces tal como los veria el buzon.
func linkRequests(t *testing.T, body string) []LinkRequest {
	t.Helper()
	var out []LinkRequest
	for _, m := range hrefRE.FindAllStringSubmatch(body, -1) {
		u, err := url.Parse(html.UnescapeString(m[1]))
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		exp, _ := strconv.ParseInt(q.Get("e"), 10, 64)
		action := domain.LinkRelease
		if u.Path == domain.LinkDiscard.Path(testCell) {
			action = domain.LinkDiscard
		} else if u.Path != domain.LinkRelease.Path(testCell) {
			t.Fatalf("el enlace lleva la celda en la ruta: %s", u.Path)
		}
		out = append(out, LinkRequest{Cell: testCell, TenantID: uuid.MustParse(q.Get("t")), QHash: q.Get("q"), ExpiresAt: exp, Signature: q.Get("sig"), Action: action})
	}
	return out
}

func TestAvisoAgrupaPorBuzonYMarcaAvisados(t *testing.T) {
	f := newNotifierFixture(t)
	anaOld := f.add("ana@acme.com", 5, 2*time.Hour, "Factura", "facturas@proveedor.com")
	anaNew := f.add("ana@acme.com", 8, time.Hour, "Oferta", "ventas@tienda.com")
	luis := f.add("luis@acme.com", 3, 30*time.Minute, "Reunion", "jefe@socio.com")
	tooSpammy := f.add("ana@acme.com", 20, 10*time.Minute, "Casino", "spam@casino.test")
	done := f.add("luis@acme.com", 2, 3*time.Hour, "Viejo", "x@y.com")
	f.q.Items[len(f.q.Items)-1].Notified = true

	res := f.sweep(t)
	if res != (SweepResult{Sent: 2}) {
		t.Fatalf("un aviso por buzon: %+v", res)
	}
	if len(f.sender.Sent) != 2 || f.sender.Tenants[0] != f.tenant {
		t.Fatalf("avisos: %+v", f.sender.Sent)
	}
	ana, luisMail := f.sender.Sent[0], f.sender.Sent[1]
	if ana.To != "ana@acme.com" || ana.From != "cuarentena@acme.com" || ana.Subject != "Correo retenido en cuarentena" ||
		ana.IdempotencyKey != "quarantine-notice:ana@acme.com:"+anaNew.ID.String() {
		t.Fatalf("aviso de ana: %+v", ana)
	}
	if luisMail.To != "luis@acme.com" || luisMail.IdempotencyKey != "quarantine-notice:luis@acme.com:"+luis.ID.String() {
		t.Fatalf("aviso de luis: %+v", luisMail)
	}
	if !strings.Contains(ana.HTML, "2 mensajes retenidos para ana@acme.com") || strings.Contains(ana.HTML, "Casino") ||
		strings.Index(ana.HTML, "Oferta") > strings.Index(ana.HTML, "Factura") {
		t.Fatalf("el aviso lista lo pendiente bajo el umbral, del mas reciente al mas antiguo:\n%s", ana.HTML)
	}
	if strings.Contains(luisMail.HTML, "Viejo") {
		t.Fatal("lo ya avisado no se repite")
	}
	for _, it := range []domain.QuarantineItem{anaOld, anaNew, luis} {
		if !f.item(it.ID).Notified {
			t.Errorf("%s debe quedar avisado", it.Subject)
		}
	}
	if f.item(tooSpammy.ID).Notified || !f.item(done.ID).Notified {
		t.Fatal("lo que supera notify_max_score no se toca")
	}
	if len(f.notices.Records) != 2 || f.notices.Records[0].Status != domain.NoticeSent || f.notices.Records[0].MessageID == nil ||
		len(f.notices.Records[0].QuarantineIDs) != 2 || f.notices.Records[0].IdempotencyKey != ana.IdempotencyKey {
		t.Fatalf("registro de avisos: %+v", f.notices.Records)
	}

	// Los enlaces del aviso son los que acepta el caso de uso de los enlaces.
	uc := NewQuarantineUseCase(QuarantineDeps{Tx: f.tx, Repo: f.q, Notices: f.notices, Links: f.links, Logger: zap.NewNop()})
	uc.now = func() time.Time { return f.now }
	reqs := linkRequests(t, ana.HTML)
	if len(reqs) != 4 || reqs[0].Action != domain.LinkRelease || reqs[1].Action != domain.LinkDiscard {
		t.Fatalf("dos enlaces por mensaje: %+v", reqs)
	}
	for _, req := range reqs {
		if req.ExpiresAt != f.now.Add(72*time.Hour).Unix() {
			t.Fatalf("caducidad: %d", req.ExpiresAt)
		}
		if _, err := uc.CheckLink(context.Background(), req); err != nil {
			t.Fatalf("enlace del aviso rechazado: %v", err)
		}
	}

	if res := f.sweep(t); res != (SweepResult{}) || len(f.sender.Sent) != 2 {
		t.Fatalf("un segundo barrido sin correo nuevo no avisa: %+v", res)
	}
	f.add("ana@acme.com", 1, 0, "Nuevo", "a@b.com")
	if res := f.sweep(t); res.Sent != 1 || !strings.Contains(f.sender.Sent[2].HTML, "1 mensajes retenidos") {
		t.Fatalf("el correo nuevo sale en su propio aviso: %+v", res)
	}
}

func TestAvisoEscapaAsuntoYRemitenteHostiles(t *testing.T) {
	f := newNotifierFixture(t)
	f.add("ana@acme.com", 5, time.Minute, `<img src=x onerror=alert(1)>`, `"><script>alert(2)</script>@evil.test`)
	f.sweep(t)
	if len(f.sender.Sent) != 1 {
		t.Fatalf("avisos: %d", len(f.sender.Sent))
	}
	body := f.sender.Sent[0].HTML
	if strings.Contains(body, "<img") || strings.Contains(body, "<script") {
		t.Fatalf("contenido hostil sin escapar:\n%s", body)
	}
	if !strings.Contains(body, "&lt;img src=x onerror=alert(1)&gt;") || !strings.Contains(body, "&lt;script&gt;alert(2)&lt;/script&gt;") {
		t.Fatalf("el texto debe verse escapado:\n%s", body)
	}
}

// Un 4xx de negocio no se reintenta en bucle: queda registrado con su codigo y los mensajes
// se dan por avisados.
func TestAviso4xxSeRegistraYNoSeReintenta(t *testing.T) {
	f := newNotifierFixture(t)
	it := f.add("ana@acme.com", 5, time.Minute, "Factura", "a@b.com")
	f.sender.Err = &ports.NoticeRejectedError{Status: 422, Code: "SENDING_DOMAIN_NOT_VERIFIED", Message: "el dominio del remitente no esta verificado"}

	if res := f.sweep(t); res != (SweepResult{Rejected: 1}) {
		t.Fatalf("rechazo: %+v", res)
	}
	if !f.item(it.ID).Notified || len(f.notices.Records) != 1 || f.notices.Records[0].Status != domain.NoticeRejected ||
		f.notices.Records[0].ErrorCode != "SENDING_DOMAIN_NOT_VERIFIED" || f.notices.Records[0].MessageID != nil {
		t.Fatalf("registro del rechazo: %+v", f.notices.Records)
	}
	if res := f.sweep(t); res != (SweepResult{}) || len(f.sender.Sent) != 1 {
		t.Fatalf("no se reintenta: %+v", res)
	}
}

// 5xx, red o 429: nada cambia y el siguiente barrido repite con la misma clave.
func TestAviso5xxSeReintentaConLaMismaClave(t *testing.T) {
	f := newNotifierFixture(t)
	it := f.add("ana@acme.com", 5, time.Minute, "Factura", "a@b.com")
	f.sender.Err = fmt.Errorf("%w: status 503", ports.ErrNoticeUnavailable)

	if res := f.sweep(t); res != (SweepResult{Retry: 1}) {
		t.Fatalf("caida: %+v", res)
	}
	if f.item(it.ID).Notified || len(f.notices.Records) != 0 {
		t.Fatal("una caida no marca ni registra")
	}
	f.now = f.now.Add(15 * time.Minute)
	f.sender.Err = nil
	if res := f.sweep(t); res != (SweepResult{Sent: 1}) {
		t.Fatalf("reintento: %+v", res)
	}
	if f.sender.Sent[0].IdempotencyKey != f.sender.Sent[1].IdempotencyKey || !f.item(it.ID).Notified {
		t.Fatalf("misma clave en el reintento: %q %q", f.sender.Sent[0].IdempotencyKey, f.sender.Sent[1].IdempotencyKey)
	}
}

func TestAvisoSuprimidoSeDaPorAtendido(t *testing.T) {
	f := newNotifierFixture(t)
	it := f.add("ana@acme.com", 5, time.Minute, "Factura", "a@b.com")
	f.sender.Suppressed = map[string]bool{"ana@acme.com": true}
	if res := f.sweep(t); res != (SweepResult{Suppressed: 1}) {
		t.Fatalf("suprimido: %+v", res)
	}
	if !f.item(it.ID).Notified || f.notices.Records[0].Status != domain.NoticeSuppressed {
		t.Fatalf("registro: %+v", f.notices.Records)
	}
}

func TestAvisoOmiteBuzonesQueNoReciben(t *testing.T) {
	f := newNotifierFixture(t)
	gone := f.add("baja@acme.com", 5, time.Minute, "a", "a@b.com")
	off := f.add("luis@acme.com", 5, time.Minute, "b", "a@b.com")
	f.dir.Mailboxes["luis@acme.com"] = domain.Mailbox{TenantID: f.tenant, Username: "luis@acme.com", Active: 0}
	if res := f.sweep(t); res != (SweepResult{Skipped: 2}) || len(f.sender.Sent) != 0 {
		t.Fatalf("buzones que no reciben: %+v enviados=%d", res, len(f.sender.Sent))
	}
	if !f.item(gone.ID).Notified || !f.item(off.ID).Notified || f.notices.Records[0].Status != domain.NoticeSkipped {
		t.Fatalf("omitidos y marcados: %+v", f.notices.Records)
	}
}

// Un aviso de cuarentena que vuelve a entrar y acaba retenido no genera otro aviso: se da
// por atendido con su motivo y el resto del buzon se avisa como siempre.
func TestAvisoNoAvisaDeSusPropiosAvisos(t *testing.T) {
	f := newNotifierFixture(t)
	own := f.add("ana@acme.com", 5, time.Minute, "Aviso anterior", "Cuarentena <CUARENTENA@acme.com>")
	other := f.add("ana@acme.com", 5, 2*time.Minute, "Factura", "proveedor@b.com")

	if res := f.sweep(t); res != (SweepResult{Sent: 1, Skipped: 1}) || len(f.sender.Sent) != 1 {
		t.Fatalf("resultado %+v, enviados %d", res, len(f.sender.Sent))
	}
	if html := f.sender.Sent[0].HTML; !strings.Contains(html, "Factura") || strings.Contains(html, "Aviso anterior") {
		t.Fatalf("el aviso debe listar solo el mensaje ajeno: %s", html)
	}
	if !f.item(own.ID).Notified || !f.item(other.ID).Notified {
		t.Fatal("los dos mensajes quedan atendidos")
	}
	found := false
	for _, r := range f.notices.Records {
		if r.ErrorCode == domain.NoticeOwnNoticeCode && r.Status == domain.NoticeSkipped {
			found = true
		}
	}
	if !found {
		t.Fatalf("falta el registro con motivo %s: %+v", domain.NoticeOwnNoticeCode, f.notices.Records)
	}
	if res := f.sweep(t); res != (SweepResult{}) {
		t.Fatalf("un segundo barrido no debe hacer nada: %+v", res)
	}
}

// El marcado y el registro van en una transaccion: si el registro falla, los mensajes
// siguen sin avisar y el siguiente barrido repite con la misma clave.
func TestAvisoMarcaYRegistraEnLaMismaTransaccion(t *testing.T) {
	f := newNotifierFixture(t)
	it := f.add("ana@acme.com", 5, time.Minute, "Factura", "a@b.com")
	f.notices.FailInsert = errors.New("insert quarantine_notices: conexion perdida")

	if res := f.sweep(t); res != (SweepResult{Retry: 1}) || len(f.sender.Sent) != 1 {
		t.Fatalf("registro fallido: %+v", res)
	}
	if f.item(it.ID).Notified || f.tx.RolledBack != 1 {
		t.Fatalf("el marcado se deshace con el registro: notified=%v rollbacks=%d", f.item(it.ID).Notified, f.tx.RolledBack)
	}
	f.notices.FailInsert = nil
	if res := f.sweep(t); res != (SweepResult{Sent: 1}) || f.sender.Sent[1].IdempotencyKey != f.sender.Sent[0].IdempotencyKey {
		t.Fatalf("repeticion con la misma clave: %+v", res)
	}
	if !f.item(it.ID).Notified || len(f.notices.Records) != 1 {
		t.Fatal("tras repetir queda marcado y registrado")
	}
}

func TestAvisoSoloEmpresasActivasYBienConfiguradas(t *testing.T) {
	f := newNotifierFixture(t)
	it := f.add("ana@acme.com", 5, time.Minute, "Factura", "a@b.com")

	s := f.policy.QSettings[f.tenant]
	s.Notify.Sender = ""
	f.policy.QSettings[f.tenant] = s
	if res := f.sweep(t); res != (SweepResult{}) || len(f.sender.Sent) != 0 || f.item(it.ID).Notified {
		t.Fatalf("sin remitente no se envia ni se marca: %+v", res)
	}
	s.Notify.Sender, s.Notify.Enabled = "cuarentena@acme.com", false
	f.policy.QSettings[f.tenant] = s
	if res := f.sweep(t); res != (SweepResult{}) || len(f.sender.Sent) != 0 {
		t.Fatalf("aviso desactivado: %+v", res)
	}
}

func TestAvisoSoloBarreConElCerrojo(t *testing.T) {
	f := newNotifierFixture(t)
	f.add("ana@acme.com", 5, time.Minute, "Factura", "a@b.com")
	f.n.tick(context.Background(), func(context.Context) (func(), bool) { return nil, false })
	if len(f.sender.Sent) != 0 || f.notices.Pruned != 0 {
		t.Fatal("sin cerrojo no se barre")
	}
	released := false
	f.n.tick(context.Background(), func(context.Context) (func(), bool) { return func() { released = true }, true })
	if len(f.sender.Sent) != 1 || !released || f.notices.Pruned != 1 {
		t.Fatalf("con cerrojo se barre, se poda el historial y se suelta: enviados=%d soltado=%v", len(f.sender.Sent), released)
	}
}
