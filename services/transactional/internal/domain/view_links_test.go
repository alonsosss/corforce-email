package domain

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func newSigner(t *testing.T) *LinkSigner {
	t.Helper()
	s, err := NewLinkSigner("0123456789abcdef0123456789abcdef-view", "https://app.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestViewInBrowserURLRoundTrip(t *testing.T) {
	s := newSigner(t)
	c := ViewClaims{TenantID: uuid.New(), MessageID: uuid.New(), ExpiresAt: time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC)}
	raw := s.ViewInBrowserURL(c)
	if !strings.HasPrefix(raw, "https://app.example.com"+ViewInBrowserPath+"?") {
		t.Fatalf("ruta inesperada: %s", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("e") != "" {
		t.Fatal("el enlace de ver en el navegador no debe llevar la direccion")
	}
	if q.Get("t") != c.TenantID.String() || q.Get("m") != c.MessageID.String() || q.Get("x") != "1796119200" {
		t.Fatalf("parametros inesperados: %v", q)
	}
	if !s.VerifyView(c, q.Get("sig")) {
		t.Fatal("la firma propia no verifica")
	}
}

func TestVerifyViewRejectsTampering(t *testing.T) {
	s := newSigner(t)
	c := ViewClaims{TenantID: uuid.New(), MessageID: uuid.New(), ExpiresAt: time.Unix(1796119200, 0)}
	sig := s.SignView(c)
	for name, alt := range map[string]ViewClaims{
		"otra caducidad": {TenantID: c.TenantID, MessageID: c.MessageID, ExpiresAt: c.ExpiresAt.Add(time.Hour)},
		"otro mensaje":   {TenantID: c.TenantID, MessageID: uuid.New(), ExpiresAt: c.ExpiresAt},
		"otra empresa":   {TenantID: uuid.New(), MessageID: c.MessageID, ExpiresAt: c.ExpiresAt},
	} {
		if s.VerifyView(alt, sig) {
			t.Fatalf("%s: una firma ajena verifico", name)
		}
	}
	if s.VerifyView(c, sig[:len(sig)-2]) {
		t.Fatal("una firma truncada verifico")
	}
}

// La firma de la baja no abre el correo ni la del correo registra una baja: cada enlace firma
// un texto con su proposito.
func TestSignaturesAreBoundToTheirPurpose(t *testing.T) {
	s := newSigner(t)
	tenant, message := uuid.New(), uuid.New()
	unsub := s.Sign(UnsubscribeClaims{TenantID: tenant, MessageID: message, Email: ""})
	if s.VerifyView(ViewClaims{TenantID: tenant, MessageID: message, ExpiresAt: time.Unix(0, 0)}, unsub) {
		t.Fatal("la firma de baja vale como enlace de ver en el navegador")
	}
	view := s.SignView(ViewClaims{TenantID: tenant, MessageID: message, ExpiresAt: time.Unix(0, 0)})
	if s.Verify(UnsubscribeClaims{TenantID: tenant, MessageID: message}, view) {
		t.Fatal("la firma de ver en el navegador vale como baja")
	}
}
