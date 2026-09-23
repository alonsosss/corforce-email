package domain

import (
	"strings"
	"testing"
)

func TestNormalizeLink(t *testing.T) {
	const email, contact, message = "Ana@Example.com", "7b0e3f6a-1c2d-4e5f-8a9b-0c1d2e3f4a5b", "0f9e8d7c-6b5a-4c3d-9e2f-1a0b9c8d7e6f"
	cases := []struct{ name, in, want string }{
		{"quita utm", "https://tienda.test/o?utm_source=x&utm_medium=email&utm_campaign=c&id=9",
			"https://tienda.test/o?id=9"},
		{"utm en mayusculas y escapado", "https://tienda.test/o?UTM_Source=x&utm%5Fcontent=y", "https://tienda.test/o"},
		{"esquema, host y puerto por defecto", "HTTPS://Tienda.TEST:443/Oferta", "https://tienda.test/Oferta"},
		{"puerto propio", "http://tienda.test:8080/o", "http://tienda.test:8080/o"},
		{"puerto 80 en http", "http://tienda.test:80", "http://tienda.test/"},
		{"ruta vacia", "https://tienda.test", "https://tienda.test/"},
		{"fragmento", "https://tienda.test/p?utm_source=x#precio", "https://tienda.test/p#precio"},
		{"credenciales fuera", "https://usuario:clave@tienda.test/o", "https://tienda.test/o"},
		{"direccion en la query", "https://tienda.test/o?e=ana%40example.com&x=1", "https://tienda.test/o?x=1"},
		{"contacto en la query", "https://tienda.test/o?c=" + contact, "https://tienda.test/o"},
		{"mensaje en la query", "https://tienda.test/o?m=" + strings.ToUpper(message) + "&a=b", "https://tienda.test/o?a=b"},
		{"direccion en la ruta", "https://tienda.test/u/ana@example.com/perfil", "https://tienda.test/u/-/perfil"},
		{"direccion en el fragmento", "https://tienda.test/o#ana@example.com", "https://tienda.test/o"},
		{"orden y formato de la query se conservan", "https://tienda.test/o?b=2&a=1&c", "https://tienda.test/o?b=2&a=1&c"},
		{"ipv6", "http://[::1]:8080/x", "http://[::1]:8080/x"},
		{"no http", "mailto:ana@example.com", ""},
		{"relativa", "/oferta", ""},
		{"ftp", "ftp://tienda.test/f", ""},
		{"opaca", "https:tienda.test", ""},
		{"rota", "https://[::1", ""},
		{"vacia", "", ""},
		{"enorme", "https://tienda.test/" + strings.Repeat("a", MaxLinkLen), ""},
	}
	for _, c := range cases {
		if got := NormalizeLink(c.in, email, contact, message); got != c.want {
			t.Errorf("%s: NormalizeLink(%q) = %q, se esperaba %q", c.name, c.in, got, c.want)
		}
	}
}

func TestNormalizeLinkIgnoraIdentificadoresCortosOVacios(t *testing.T) {
	if got := NormalizeLink("https://tienda.test/a?x=ab", "", "ab", "  "); got != "https://tienda.test/a?x=ab" {
		t.Fatalf("un identificador de menos de 3 caracteres no borra nada: %q", got)
	}
}

func TestLinkHashDistingueURL(t *testing.T) {
	a, b := LinkHash("https://tienda.test/a"), LinkHash("https://tienda.test/b")
	if len(a) != 32 || string(a) == string(b) || string(LinkHash("https://tienda.test/a")) != string(a) {
		t.Fatal("sha256 estable y distinto por URL")
	}
}

func TestParseLinksLimit(t *testing.T) {
	if v, err := ParseLinksLimit(""); err != nil || v != DefaultLinksLimit {
		t.Fatalf("por defecto: %d %v", v, err)
	}
	if v, err := ParseLinksLimit("501"); err != nil || v != MaxLinksLimit {
		t.Fatalf("maximo: %d %v", v, err)
	}
	for _, bad := range []string{"0", "-1", "502", "x", "1.5"} {
		if _, err := ParseLinksLimit(bad); !IsValidation(err) {
			t.Errorf("%q deberia rechazarse", bad)
		}
	}
}
