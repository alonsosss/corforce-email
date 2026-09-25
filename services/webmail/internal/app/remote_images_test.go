package app

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

const testMailboxID = "22222222-2222-4222-8222-222222222222"

var testPNG = []byte("\x89PNG\r\n\x1a\nresto")

type fakeFetcher struct {
	data  []byte
	err   error
	calls []string
}

func (f *fakeFetcher) Fetch(_ context.Context, rawURL string) ([]byte, error) {
	f.calls = append(f.calls, rawURL)
	return f.data, f.err
}

func proxyURL(s domain.SignedRemoteImage) string {
	q := url.Values{"u": {s.EncodedURL}, "x": {s.Expires}, "m": {s.MailboxID}, "s": {s.Signature}}
	return "/proxy?" + q.Encode()
}

// withImageProxy rehace el servicio del arnes con el proxy de imagenes.
func withImageProxy(t *testing.T, h *harness) *fakeFetcher {
	t.Helper()
	f := &fakeFetcher{data: testPNG}
	d := h.deps()
	d.ImageProxy = &ImageProxyDeps{Fetcher: f, URL: proxyURL, Key: domain.DeriveRemoteImageKey([]byte("secreto-de-pruebas-de-32-caracteres")), TTL: time.Hour}
	svc, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	return f
}

func signedFromURL(t *testing.T, raw string) domain.SignedRemoteImage {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	return domain.SignedRemoteImage{EncodedURL: q.Get("u"), Expires: q.Get("x"), MailboxID: q.Get("m"), Signature: q.Get("s")}
}

func TestLeerConImagenesPermitidasLasFirmaParaElProxy(t *testing.T) {
	h := newHarness(t)
	withImageProxy(t, h)
	sess := domain.Session{Username: testUser, MailboxID: testMailboxID}
	h.mb.raw = &domain.RawMessage{HTML: "<p>x</p>"}
	h.sanitizer.remote = true
	h.sanitizer.remoteURL = "https://tracker.test/p.gif"

	blocked, err := h.svc.ReadMessage(ctx, sess, "INBOX", 9, true, false)
	if err != nil || !blocked.RemoteImages.Blocked || blocked.RemoteImages.Proxied || strings.Contains(blocked.HTML, "/proxy") {
		t.Fatalf("sin permiso nada pasa por el proxy: %+v %v", blocked, err)
	}

	msg, err := h.svc.ReadMessage(ctx, sess, "INBOX", 9, true, true)
	if err != nil || msg.RemoteImages.Blocked || !msg.RemoteImages.Proxied {
		t.Fatalf("con permiso, por el proxy: %+v %v", msg.RemoteImages, err)
	}
	_, proxied, _ := strings.Cut(msg.HTML, "|")
	link, err := h.svc.VerifyRemoteImage(signedFromURL(t, proxied))
	if err != nil || link.URL != "https://tracker.test/p.gif" || link.MailboxID != testMailboxID {
		t.Fatalf("el enlace firmado vale para el proxy: %+v %v", link, err)
	}
	if left := link.Expires.Sub(h.clock.Now()); left < time.Hour || left > time.Hour+15*time.Minute {
		t.Fatalf("caduca entre ttl y ttl y un cuarto: %v", left)
	}
}

func TestSesionSinBuzonNoMuestraImagenesRemotas(t *testing.T) {
	h := newHarness(t)
	withImageProxy(t, h)
	h.mb.raw = &domain.RawMessage{HTML: "<p>x</p>"}
	h.sanitizer.remote = true
	h.sanitizer.remoteURL = "https://tracker.test/p.gif"
	msg, err := h.svc.ReadMessage(ctx, domain.Session{Username: testUser}, "INBOX", 9, true, true)
	if err != nil || !msg.RemoteImages.Blocked || h.sanitizer.opts.ProxyRemoteImage != nil {
		t.Fatalf("sin buzon no hay cupo que cargar: %+v %v", msg.RemoteImages, err)
	}
}

func TestVerificarEnlaceAlteradoCaducadoODeOtroBuzon(t *testing.T) {
	h := newHarness(t)
	withImageProxy(t, h)
	now := h.clock.Now()
	link, err := domain.NewRemoteImageLink("https://x.test/a.png", testMailboxID, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	key := domain.DeriveRemoteImageKey([]byte("secreto-de-pruebas-de-32-caracteres"))
	good := link.Sign(key)
	if _, err := h.svc.VerifyRemoteImage(good); err != nil {
		t.Fatal(err)
	}
	other := good
	other.MailboxID = "33333333-3333-4333-8333-333333333333"
	otherURL := good
	otherURL.EncodedURL = domain.RemoteImageLink{URL: "https://x.test/b.png", MailboxID: testMailboxID, Expires: link.Expires}.Sign(key).EncodedURL
	later := good
	later.Expires = "9999999999"
	wrongKey := link.Sign(domain.DeriveRemoteImageKey([]byte("otra-clave-de-pruebas-de-32-caracteres")))
	for name, s := range map[string]domain.SignedRemoteImage{"otro buzon": other, "otra URL": otherURL, "caducidad alargada": later, "otra clave": wrongKey} {
		if _, err := h.svc.VerifyRemoteImage(s); !errors.Is(err, domain.ErrRemoteImageLinkInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	h.clock.Advance(2 * time.Hour)
	if _, err := h.svc.VerifyRemoteImage(good); !errors.Is(err, domain.ErrRemoteImageLinkExpired) {
		t.Fatalf("caducado: %v", err)
	}
}

func TestDescargaSoloMapasDeBits(t *testing.T) {
	h := newHarness(t)
	f := withImageProxy(t, h)
	link := domain.RemoteImageLink{URL: "https://x.test/a.png", MailboxID: testMailboxID}
	img, err := h.svc.FetchRemoteImage(ctx, link)
	if err != nil || img.ContentType != "image/png" || string(img.Data) != string(testPNG) {
		t.Fatalf("%+v %v", img, err)
	}
	for name, data := range map[string][]byte{
		"svg":   []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"html":  []byte("<html><body>hola</body></html>"),
		"vacio": nil,
	} {
		f.data = data
		if _, err := h.svc.FetchRemoteImage(ctx, link); !errors.Is(err, domain.ErrRemoteImageNotImage) {
			t.Errorf("%s: %v", name, err)
		}
	}
	f.err = domain.ErrRemoteImageTooLarge
	if _, err := h.svc.FetchRemoteImage(ctx, link); !errors.Is(err, domain.ErrRemoteImageTooLarge) {
		t.Fatalf("tope: %v", err)
	}
}

func TestSinProxyNingunEnlaceVale(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.VerifyRemoteImage(domain.SignedRemoteImage{}); !errors.Is(err, domain.ErrRemoteImageLinkInvalid) {
		t.Fatalf("%v", err)
	}
}

func TestProxyMalConfiguradoNoArranca(t *testing.T) {
	h := newHarness(t)
	for name, p := range map[string]*ImageProxyDeps{
		"sin descarga": {URL: proxyURL, Key: make([]byte, 32), TTL: time.Hour},
		"clave corta":  {Fetcher: &fakeFetcher{}, URL: proxyURL, Key: make([]byte, 16), TTL: time.Hour},
		"sin plazo":    {Fetcher: &fakeFetcher{}, URL: proxyURL, Key: make([]byte, 32)},
	} {
		d := h.deps()
		d.ImageProxy = p
		if _, err := New(d); err == nil {
			t.Errorf("%s: arranca", name)
		}
	}
}
