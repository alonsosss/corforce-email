package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

type linkFixture struct {
	q       *apptest.Quarantine
	notices *apptest.Notices
	pub     *apptest.Publisher
	reinj   *reinyectorFalso
	links   *domain.QuarantineLinkSigner
	uc      *QuarantineUseCase
	item    domain.QuarantineItem
	now     time.Time
}

func newLinkFixture(t *testing.T) *linkFixture {
	t.Helper()
	links, err := domain.NewQuarantineLinkSigner(testLinkKey, "https://app.example.com", testCell, 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	q := &apptest.Quarantine{}
	notices := apptest.NewNotices(q)
	tx := &apptest.Tx{Snapshot: notices.Snapshot}
	f := &linkFixture{q: q, notices: notices, pub: &apptest.Publisher{Tx: tx}, reinj: &reinyectorFalso{}, links: links,
		now: time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)}
	f.uc = NewQuarantineUseCase(QuarantineDeps{Tx: tx, Repo: q, Reinjector: f.reinj, Events: f.pub,
		Notices: notices, Links: links, Logger: zap.NewNop()})
	f.uc.now = func() time.Time { return f.now }
	id := uuid.New()
	sum := sha256.Sum256([]byte(id.String() + "QID1"))
	f.item = domain.QuarantineItem{ID: id, TenantID: uuid.New(), Rcpt: "ana@acme.com", Sender: "x@y.com", Subject: "Factura",
		Score: decimal.NewFromInt(9), QHash: hex.EncodeToString(sum[:]), Msg: []byte("m")}
	q.Items = []domain.QuarantineItem{f.item}
	return f
}

func (f *linkFixture) link(action domain.QuarantineLinkAction, expires time.Time) LinkRequest {
	claims := domain.QuarantineLinkClaims{TenantID: f.item.TenantID, MessageID: f.item.ID, Action: action, ExpiresAt: expires.Unix()}
	return LinkRequest{Cell: testCell, TenantID: f.item.TenantID, QHash: f.item.QHash, ExpiresAt: claims.ExpiresAt, Signature: f.links.Sign(claims), Action: action}
}

// legacyLink es un enlace sin celda de los emitidos antes de llevarla en la ruta.
func (f *linkFixture) legacyLink(action domain.QuarantineLinkAction, expires time.Time) LinkRequest {
	claims := domain.QuarantineLinkClaims{TenantID: f.item.TenantID, MessageID: f.item.ID, Action: action, ExpiresAt: expires.Unix()}
	return LinkRequest{Legacy: true, TenantID: f.item.TenantID, QHash: f.item.QHash, ExpiresAt: claims.ExpiresAt,
		Signature: apptest.LegacyLinkSignature(testLinkKey, claims), Action: action}
}

var testClient = LinkClient{IP: "203.0.113.7", UserAgent: "Mozilla/5.0"}

func TestEnlaceValidoLiberaConElCasoDeUsoDeSiempre(t *testing.T) {
	f := newLinkFixture(t)
	req := f.link(domain.LinkRelease, f.now.Add(time.Hour))
	got, err := f.uc.CheckLink(context.Background(), req)
	if err != nil || got.ID != f.item.ID {
		t.Fatalf("comprobar: %+v %v", got, err)
	}
	if err := f.uc.ReleaseByLink(context.Background(), req, testClient); err != nil {
		t.Fatal(err)
	}
	if f.reinj.entregados != 1 || len(f.q.Items) != 0 || len(f.pub.Subjects) != 1 || f.pub.Subjects[0] != domain.SubjectQuarantineReleased || len(f.pub.Outside) != 0 {
		t.Fatalf("liberacion: entregas=%d filas=%d eventos=%v fuera=%v", f.reinj.entregados, len(f.q.Items), f.pub.Subjects, f.pub.Outside)
	}
	use, ok := f.notices.Uses[f.item.ID]
	if !ok || use.Action != domain.LinkRelease || use.ClientIP != testClient.IP || use.UserAgent != testClient.UserAgent || use.Rcpt != "ana@acme.com" || !use.UsedAt.Equal(f.now) {
		t.Fatalf("constancia del uso: %+v", use)
	}
}

// Un enlace es de un solo uso: repetido, o usado el otro enlace del mismo mensaje, ya no
// vale; y un uso registrado basta aunque la fila siguiera ahi.
func TestEnlaceDeUnSoloUso(t *testing.T) {
	f := newLinkFixture(t)
	release := f.link(domain.LinkRelease, f.now.Add(time.Hour))
	discard := f.link(domain.LinkDiscard, f.now.Add(time.Hour))
	if err := f.uc.ReleaseByLink(context.Background(), release, testClient); err != nil {
		t.Fatal(err)
	}
	if err := f.uc.ReleaseByLink(context.Background(), release, testClient); !errors.Is(err, domain.ErrInvalidLink) {
		t.Fatalf("segundo uso: %v", err)
	}
	if err := f.uc.DiscardByLink(context.Background(), discard, testClient); !errors.Is(err, domain.ErrInvalidLink) {
		t.Fatalf("el otro enlace del mismo mensaje: %v", err)
	}
	if _, err := f.uc.CheckLink(context.Background(), release); !errors.Is(err, domain.ErrInvalidLink) {
		t.Fatalf("la pagina de un enlace usado: %v", err)
	}

	g := newLinkFixture(t)
	g.notices.Uses[g.item.ID] = domain.QuarantineLinkUse{TenantID: g.item.TenantID, QuarantineID: g.item.ID, Action: domain.LinkDiscard}
	if err := g.uc.ReleaseByLink(context.Background(), g.link(domain.LinkRelease, g.now.Add(time.Hour)), testClient); !errors.Is(err, domain.ErrInvalidLink) {
		t.Fatalf("uso ya registrado: %v", err)
	}
	if g.reinj.entregados != 0 || len(g.q.Items) != 1 || len(g.pub.Subjects) != 0 {
		t.Fatal("con el uso registrado no se entrega ni se borra nada")
	}
}

func TestEnlaceAlteradoOCaducadoNoValeYNoTocaNada(t *testing.T) {
	f := newLinkFixture(t)
	valid := f.link(domain.LinkRelease, f.now.Add(time.Hour))
	flipped := []byte(valid.Signature)
	if flipped[0] == 'a' {
		flipped[0] = 'b'
	} else {
		flipped[0] = 'a'
	}
	cases := map[string]LinkRequest{}
	r := valid
	r.Signature = string(flipped)
	cases["firma alterada"] = r
	r = valid
	r.ExpiresAt = f.now.Add(2 * time.Hour).Unix()
	cases["caducidad alargada"] = r
	cases["caducado"] = f.link(domain.LinkRelease, f.now.Add(-time.Second))
	cases["justo al caducar"] = f.link(domain.LinkRelease, f.now)
	r = valid
	r.TenantID = uuid.New()
	cases["otra empresa"] = r
	r = valid
	r.QHash = strings.Repeat("0", 64)
	cases["qhash desconocido"] = r
	r = valid
	r.QHash = "no-es-hex"
	cases["qhash malformado"] = r
	r = valid
	r.Action = domain.LinkDiscard
	cases["firma de liberar en descartar"] = r
	r = valid
	r.Cell = "pe-02"
	cases["otra celda en la ruta"] = r
	r = valid
	r.Cell = "zz-99"
	cases["celda desconocida"] = r
	r = valid
	r.Cell = ""
	cases["sin celda en la ruta"] = r
	pe02, err := domain.NewQuarantineLinkSigner(testLinkKey, "https://app.example.com", "pe-02", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r = valid
	r.Signature = pe02.Sign(domain.QuarantineLinkClaims{TenantID: f.item.TenantID, MessageID: f.item.ID, Action: domain.LinkRelease, ExpiresAt: valid.ExpiresAt})
	cases["firmado en otra celda con el segmento cambiado"] = r
	r = valid
	r.Legacy = true
	cases["firma con celda en la ruta sin celda"] = r
	r = f.legacyLink(domain.LinkRelease, f.now.Add(time.Hour))
	r.Legacy = false
	r.Cell = testCell
	cases["firma sin celda en la ruta con celda"] = r
	cases["sin celda caducado"] = f.legacyLink(domain.LinkRelease, f.now.Add(-time.Second))
	r = f.legacyLink(domain.LinkRelease, f.now.Add(time.Hour))
	r.TenantID = uuid.New()
	cases["sin celda de otra empresa"] = r

	for name, req := range cases {
		if _, err := f.uc.CheckLink(context.Background(), req); !errors.Is(err, domain.ErrInvalidLink) {
			t.Errorf("%s: la pagina debe rechazarlo: %v", name, err)
		}
		var err error
		if req.Action == domain.LinkDiscard {
			err = f.uc.DiscardByLink(context.Background(), req, testClient)
		} else {
			err = f.uc.ReleaseByLink(context.Background(), req, testClient)
		}
		if !errors.Is(err, domain.ErrInvalidLink) {
			t.Errorf("%s: la accion debe rechazarlo: %v", name, err)
		}
	}
	if f.reinj.entregados != 0 || len(f.q.Items) != 1 || len(f.notices.Uses) != 0 {
		t.Fatalf("nada cambia: entregas=%d filas=%d usos=%d", f.reinj.entregados, len(f.q.Items), len(f.notices.Uses))
	}
	if err := f.uc.DiscardByLink(context.Background(), valid, testClient); !errors.Is(err, domain.ErrInvalidLink) {
		t.Fatalf("un enlace de liberar no descarta: %v", err)
	}
}

// Los enlaces sin celda ya enviados siguen liberando y descartando hasta que caducan, con
// el mismo uso unico que los nuevos.
func TestEnlaceSinCeldaDeAntesValeHastaCaducar(t *testing.T) {
	f := newLinkFixture(t)
	release := f.legacyLink(domain.LinkRelease, f.now.Add(time.Hour))
	if _, err := f.uc.CheckLink(context.Background(), release); err != nil {
		t.Fatalf("pagina de un enlace de antes: %v", err)
	}
	if err := f.uc.ReleaseByLink(context.Background(), release, testClient); err != nil {
		t.Fatal(err)
	}
	if f.reinj.entregados != 1 || len(f.q.Items) != 0 || f.notices.Uses[f.item.ID].Action != domain.LinkRelease {
		t.Fatalf("liberacion: entregas=%d filas=%d usos=%+v", f.reinj.entregados, len(f.q.Items), f.notices.Uses)
	}
	if err := f.uc.ReleaseByLink(context.Background(), release, testClient); !errors.Is(err, domain.ErrInvalidLink) {
		t.Fatalf("segundo uso: %v", err)
	}

	g := newLinkFixture(t)
	if err := g.uc.DiscardByLink(context.Background(), g.legacyLink(domain.LinkDiscard, g.now.Add(time.Hour)), testClient); err != nil {
		t.Fatal(err)
	}
	if len(g.q.Items) != 0 || g.reinj.entregados != 0 {
		t.Fatalf("descarte: filas=%d entregas=%d", len(g.q.Items), g.reinj.entregados)
	}
}

func TestEnlaceDescartaBorraSinEntregar(t *testing.T) {
	f := newLinkFixture(t)
	if err := f.uc.DiscardByLink(context.Background(), f.link(domain.LinkDiscard, f.now.Add(time.Hour)), testClient); err != nil {
		t.Fatal(err)
	}
	if len(f.q.Items) != 0 || f.reinj.entregados != 0 || len(f.pub.Subjects) != 0 || f.notices.Uses[f.item.ID].Action != domain.LinkDiscard {
		t.Fatalf("descarte: filas=%d entregas=%d eventos=%v usos=%+v", len(f.q.Items), f.reinj.entregados, f.pub.Subjects, f.notices.Uses)
	}
}

// Si la reinyeccion falla, la transaccion se deshace entera, uso incluido: el mismo enlace
// sirve para reintentar.
func TestEnlaceConReinyeccionFallidaSePuedeReintentar(t *testing.T) {
	f := newLinkFixture(t)
	req := f.link(domain.LinkRelease, f.now.Add(time.Hour))
	f.reinj.fallo = errors.New("postfix no responde")
	err := f.uc.ReleaseByLink(context.Background(), req, testClient)
	if err == nil || errors.Is(err, domain.ErrInvalidLink) {
		t.Fatalf("una caida no es un enlace invalido: %v", err)
	}
	if len(f.q.Items) != 1 || len(f.notices.Uses) != 0 {
		t.Fatalf("nada queda: filas=%d usos=%d", len(f.q.Items), len(f.notices.Uses))
	}
	f.reinj.fallo = nil
	if err := f.uc.ReleaseByLink(context.Background(), req, testClient); err != nil {
		t.Fatalf("reintento: %v", err)
	}
}

func TestEnlaceSinFirmanteEsInvalido(t *testing.T) {
	f := newLinkFixture(t)
	req := f.link(domain.LinkRelease, f.now.Add(time.Hour))
	uc := NewQuarantineUseCase(QuarantineDeps{Tx: &apptest.Tx{}, Repo: f.q, Reinjector: f.reinj, Events: f.pub, Notices: f.notices, Logger: zap.NewNop()})
	if err := uc.ReleaseByLink(context.Background(), req, testClient); !errors.Is(err, domain.ErrInvalidLink) {
		t.Fatalf("sin MAIL_LINK_SIGNING_KEY no hay enlaces: %v", err)
	}
}
