package totp

import (
	"net/url"
	"strings"
	"testing"
	"time"

	pkgtotp "github.com/alonsosss/corforce-email/pkg/totp"
)

func TestProvisionerUsaElEmisorDelDespliegue(t *testing.T) {
	p, err := New(" Core Force Mail ")
	if err != nil {
		t.Fatal(err)
	}
	secret, err := p.NewSecret()
	if err != nil || len(secret) != 32 {
		t.Fatalf("secreto de 160 bits en base32: %q %v", secret, err)
	}
	if other, _ := p.NewSecret(); other == secret {
		t.Fatal("cada secreto es nuevo")
	}
	u, err := url.Parse(p.ProvisioningURI(secret, "ana@empresa.pe"))
	if err != nil || u.Scheme != "otpauth" || u.Host != "totp" {
		t.Fatalf("%v %v", u, err)
	}
	q := u.Query()
	if q.Get("issuer") != "Core Force Mail" || q.Get("secret") != secret || q.Get("digits") != "6" || q.Get("period") != "30" {
		t.Fatalf("parametros: %v", q)
	}
	if !strings.HasSuffix(u.Path, "Core Force Mail:ana@empresa.pe") {
		t.Fatalf("etiqueta: %q", u.Path)
	}
	code, _ := pkgtotp.Generate(secret, time.Now())
	if !pkgtotp.Validate(secret, code) {
		t.Fatal("el secreto sirve para generar y validar codigos")
	}
}

func TestProvisionerRechazaEmisoresInvalidos(t *testing.T) {
	for _, issuer := range []string{"", "   ", "Core:Force", strings.Repeat("a", MaxIssuerLen+1), "Core\nForce"} {
		if _, err := New(issuer); err == nil {
			t.Errorf("%q deberia rechazarse", issuer)
		}
	}
}
