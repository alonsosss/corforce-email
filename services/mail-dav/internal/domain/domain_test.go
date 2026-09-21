package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

var testLimits = Limits{MaxVCardBytes: 4096, MaxVCardProperties: 20, MaxContactsPerMailbox: 10, MaxAddressbooksPerMailbox: 3, MaxChangesRetained: 5}

const vcard30 = "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:abc-123\r\nFN:Ana Perez\r\nN:Perez;Ana;;;\r\nEMAIL;TYPE=WORK:ana@acme.test\r\nEMAIL;TYPE=HOME:Ana@Acme.test\r\nTEL;TYPE=CELL:+51 999 111 222\r\nEND:VCARD\r\n"

func vcardWith(lines ...string) string {
	return "BEGIN:VCARD\r\n" + strings.Join(lines, "\r\n") + "\r\nEND:VCARD\r\n"
}

func TestParseVCardAceptaTarjetasReales(t *testing.T) {
	cases := map[string]string{
		"3.0 de iOS":             vcard30,
		"4.0":                    vcardWith("VERSION:4.0", "UID:urn:uuid:1f2e", "FN:Bea", "EMAIL:bea@acme.test"),
		"solo LF":                "BEGIN:VCARD\nVERSION:3.0\nUID:u1\nFN:Cris\nEND:VCARD\n",
		"minusculas":             "begin:vcard\nversion:3.0\nuid:u2\nfn:Dani\nend:vcard",
		"lineas plegadas":        vcardWith("VERSION:3.0", "UID:u3", "FN:Nombre muy", " \tlargo dividido", "NOTE:una nota"),
		"grupo con parametros":   vcardWith("VERSION:3.0", "UID:u4", "FN:Eva", `item1.URL;X-ABLabel="a:b":https://acme.test`),
		"lineas en blanco":       "BEGIN:VCARD\r\n\r\nVERSION:3.0\r\nUID:u5\r\nFN:Fer\r\n\r\nEND:VCARD\r\n\r\n",
		"caracteres no ASCII":    vcardWith("VERSION:3.0", "UID:u6", "FN:Ñandú Ñuñoa é"),
		"sin FN pero con N":      vcardWith("VERSION:3.0", "UID:u7", "N:Gomez;Gala;;;"),
		"propiedad X- privada":   vcardWith("VERSION:3.0", "UID:u8", "FN:Hugo", "X-ABUID:9A1B:ABPerson"),
		"valor con dos puntos":   vcardWith("VERSION:3.0", "UID:u9", "FN:Ivo", "URL:https://acme.test:8443/x"),
		"foto en base64 plegada": vcardWith("VERSION:3.0", "UID:u10", "FN:Jaz", "PHOTO;ENCODING=b;TYPE=JPEG:/9j/4AAQSkZJRgABAQ", " AAAAAAAAAAAAAAAAAA"),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseVCard(raw, testLimits); err != nil {
				t.Fatalf("debia aceptarse: %v", err)
			}
		})
	}
}

func TestParseVCardRechazaLoQueNoEsTarjetaValidaNiAcotada(t *testing.T) {
	long := vcardWith(append([]string{"VERSION:3.0", "UID:x", "FN:x"}, repeat("NOTE:y", 30)...)...)
	cases := map[string]string{
		"vacio":                      "",
		"sin BEGIN":                  "VERSION:3.0\r\nUID:x\r\nFN:x\r\nEND:VCARD\r\n",
		"sin END":                    "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:x\r\nFN:x\r\n",
		"otro objeto":                "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nUID:x\r\nEND:VCALENDAR\r\n",
		"version 2.1":                vcardWith("VERSION:2.1", "UID:x", "FN:x"),
		"sin version":                vcardWith("UID:x", "FN:x"),
		"version repetida":           vcardWith("VERSION:3.0", "VERSION:4.0", "UID:x", "FN:x"),
		"sin UID":                    vcardWith("VERSION:3.0", "FN:x"),
		"UID vacio":                  vcardWith("VERSION:3.0", "UID:   ", "FN:x"),
		"UID repetido":               vcardWith("VERSION:3.0", "UID:a", "UID:b", "FN:x"),
		"UID enorme":                 vcardWith("VERSION:3.0", "UID:"+strings.Repeat("a", 256), "FN:x"),
		"tarjeta anidada":            vcardWith("VERSION:3.0", "UID:x", "BEGIN:VCARD", "END:VCARD"),
		"dos tarjetas":               vcard30 + vcard30,
		"linea sin dos puntos":       vcardWith("VERSION:3.0", "UID:x", "FN"),
		"nombre con espacio":         vcardWith("VERSION:3.0", "UID:x", "F N:x"),
		"nombre vacio":               vcardWith("VERSION:3.0", "UID:x", ":valor"),
		"caracter de control":        vcardWith("VERSION:3.0", "UID:x", "FN:a\x01b"),
		"NUL":                        vcardWith("VERSION:3.0", "UID:x", "FN:a\x00b"),
		"CR suelto":                  "BEGIN:VCARD\rVERSION:3.0\r\nUID:x\r\nFN:x\r\nEND:VCARD\r\n",
		"UTF-8 invalido":             vcardWith("VERSION:3.0", "UID:x", "FN:\xff\xfe"),
		"BOM inicial":                "\xef\xbb\xbf" + vcard30,
		"demasiadas propiedades":     long,
		"DEL":                        vcardWith("VERSION:3.0", "UID:x", "FN:a\x7fb"),
		"continuacion sin propiedad": " continuacion\r\n" + vcard30,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseVCard(raw, testLimits)
			var bad *VCardError
			if !errors.As(err, &bad) || bad.TooLarge {
				t.Fatalf("debia ser un VCardError de contenido, fue %v", err)
			}
		})
	}
}

func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

func TestParseVCardExcesoDeTamano(t *testing.T) {
	raw := vcardWith("VERSION:3.0", "UID:x", "FN:x", "NOTE:"+strings.Repeat("a", testLimits.MaxVCardBytes))
	_, err := ParseVCard(raw, testLimits)
	var bad *VCardError
	if !errors.As(err, &bad) || !bad.TooLarge {
		t.Fatalf("debia ser TooLarge: %v", err)
	}
	if _, err := ParseStoredVCard(raw); err != nil {
		t.Fatalf("un vCard ya guardado se relee sin los topes de hoy: %v", err)
	}
}

func TestNewContactIndexaCamposYEtag(t *testing.T) {
	c, err := NewContact("ana.vcf", vcard30, testLimits)
	if err != nil {
		t.Fatal(err)
	}
	if c.UID != "abc-123" || c.DisplayName != "Ana Perez" || c.VCard != vcard30 || c.ETag != ETagOf(vcard30) || len(c.ETag) != 64 {
		t.Fatalf("contacto: %+v", c)
	}
	if len(c.Emails) != 1 || c.Emails[0] != "ana@acme.test" {
		t.Fatalf("correos: %v", c.Emails)
	}
	if _, err := NewContact("../ana.vcf", vcard30, testLimits); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("un nombre de recurso con ruta debe rechazarse: %v", err)
	}
	if ETagOf(vcard30) == ETagOf(vcard30+" ") {
		t.Fatal("el etag debe depender de los bytes exactos")
	}
}

func TestNombreVisibleCaeAlSiguienteDato(t *testing.T) {
	cases := []struct {
		lines []string
		want  string
	}{
		{[]string{"UID:u", "FN:Ana"}, "Ana"},
		{[]string{"UID:u", "FN:  ", "N:Gomez;Gala;;;"}, "Gomez Gala"},
		{[]string{"UID:u", "ORG:Acme;Ventas"}, "Acme Ventas"},
		{[]string{"UID:u", "EMAIL:x@acme.test"}, "x@acme.test"},
		{[]string{"UID:u"}, "u"},
	}
	for _, c := range cases {
		card, err := ParseVCard(vcardWith(append([]string{"VERSION:3.0"}, c.lines...)...), testLimits)
		if err != nil {
			t.Fatal(err)
		}
		if got := card.DisplayName(); got != c.want {
			t.Errorf("%v: %q, quiero %q", c.lines, got, c.want)
		}
	}
	long := vcardWith("VERSION:3.0", "UID:u", "FN:"+strings.Repeat("ñ", 500))
	card, _ := ParseVCard(long, Limits{MaxVCardBytes: 1 << 20, MaxVCardProperties: 5})
	if n := len([]rune(card.DisplayName())); n != MaxContactDisplayRunes {
		t.Errorf("el nombre visible se acota a %d caracteres, fue %d", MaxContactDisplayRunes, n)
	}
}

func TestNombresValidos(t *testing.T) {
	for _, s := range []string{"contacts", "a", "familia-2", strings.Repeat("a", 63)} {
		if !ValidSlug(s) {
			t.Errorf("slug %q debia ser valido", s)
		}
	}
	for _, s := range []string{"", "-a", "A", "a_b", "a/b", "..", "a b", strings.Repeat("a", 64)} {
		if ValidSlug(s) {
			t.Errorf("slug %q debia rechazarse", s)
		}
	}
	for _, s := range []string{"a.vcf", "1f2e-3.vcf", "ABC_def@x=+~.vcf"} {
		if !ValidResourceName(s) {
			t.Errorf("recurso %q debia ser valido", s)
		}
	}
	for _, s := range []string{"", ".vcf", "a", "a.ics", "../a.vcf", "a/b.vcf", `a\b.vcf`, "a b.vcf", "a.vcf/", "%2e%2e.vcf", ".hidden.vcf", "a\x00.vcf", "a\n.vcf"} {
		if ValidResourceName(s) {
			t.Errorf("recurso %q debia rechazarse", s)
		}
	}
	if u, ok := NormalizeUsername("  Ana@Acme.TEST "); !ok || u != "ana@acme.test" {
		t.Errorf("normalizacion: %q %v", u, ok)
	}
	for _, s := range []string{"", "ana", "@acme.test", "ana@", "a b@acme.test", "a/b@acme.test", "a\x00@acme.test"} {
		if _, ok := NormalizeUsername(s); ok {
			t.Errorf("%q no es un buzon", s)
		}
	}
}

func TestTokenDeSincronizacion(t *testing.T) {
	book := uuid.New()
	id, seq, initial, err := ParseSyncToken(SyncToken(book, 42))
	if err != nil || initial || id != book || seq != 42 {
		t.Fatalf("ida y vuelta: %v %d %v %v", id, seq, initial, err)
	}
	if _, _, initial, err := ParseSyncToken("  "); err != nil || !initial {
		t.Fatalf("el token vacio es la sincronizacion inicial: %v %v", initial, err)
	}
	for _, bad := range []string{
		"42", "urn:mail-dav:sync:42", "urn:mail-dav:sync:" + book.String(), "urn:mail-dav:sync:" + book.String() + ":-1",
		"urn:mail-dav:sync:" + book.String() + ":01", "urn:mail-dav:sync:" + book.String() + ":x",
		"urn:mail-dav:sync:" + strings.ToUpper(book.String()) + ":1", "http://otro/sync/1",
		"urn:mail-dav:sync:" + book.String() + ":99999999999999999999",
	} {
		if _, _, _, err := ParseSyncToken(bad); !errors.Is(err, ErrInvalidSyncToken) {
			t.Errorf("%q debia ser invalido: %v", bad, err)
		}
	}
}

func TestPrecondiciones(t *testing.T) {
	etag := "abc"
	other := "zzz"
	cases := []struct {
		name    string
		cond    Precondition
		current *string
		fails   bool
	}{
		{"sin condicion, no existe", Precondition{}, nil, false},
		{"sin condicion, existe", Precondition{}, &etag, false},
		{"If-None-Match * crea", Precondition{IfNoneMatchAny: true}, nil, false},
		{"If-None-Match * sobre existente", Precondition{IfNoneMatchAny: true}, &etag, true},
		{"If-Match * sobre inexistente", Precondition{IfMatchAny: true}, nil, true},
		{"If-Match * sobre existente", Precondition{IfMatchAny: true}, &etag, false},
		{"If-Match coincide", Precondition{IfMatch: []string{"x", "abc"}}, &etag, false},
		{"If-Match no coincide", Precondition{IfMatch: []string{"x"}}, &etag, true},
		{"If-Match sobre inexistente", Precondition{IfMatch: []string{"abc"}}, nil, true},
		{"If-None-Match con el etag actual", Precondition{IfNoneMatch: []string{"abc"}}, &etag, true},
		{"If-None-Match con otro etag", Precondition{IfNoneMatch: []string{"abc"}}, &other, false},
		{"If-None-Match sobre inexistente", Precondition{IfNoneMatch: []string{"abc"}}, nil, false},
	}
	for _, c := range cases {
		err := c.cond.Check(c.current)
		if (err != nil) != c.fails || (err != nil && !errors.Is(err, ErrPreconditionFailed)) {
			t.Errorf("%s: err=%v, fallo esperado=%v", c.name, err, c.fails)
		}
	}
}

func card(t *testing.T, raw string) Card {
	t.Helper()
	c, err := ParseVCard(raw, testLimits)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFiltrosDeConsulta(t *testing.T) {
	ana := card(t, vcard30)
	bea := card(t, vcardWith("VERSION:4.0", "UID:b", "FN:Bea Ruiz", "EMAIL:bea@otra.test"))
	match := func(name, text string, typ MatchType, negate bool) PropFilter {
		return PropFilter{Name: name, Matches: []TextMatch{{Text: text, Collation: CollationUnicodeCasemap, Type: typ, Negate: negate}}}
	}
	cases := []struct {
		name   string
		filter Filter
		ana    bool
		bea    bool
	}{
		{"sin filtros admite todo", Filter{}, true, true},
		{"FN contiene", Filter{Props: []PropFilter{match("FN", "PEREZ", MatchContains, false)}}, true, false},
		{"FN igual", Filter{Props: []PropFilter{match("FN", "bea ruiz", MatchEquals, false)}}, false, true},
		{"EMAIL empieza", Filter{Props: []PropFilter{match("EMAIL", "ana@", MatchStartsWith, false)}}, true, false},
		{"EMAIL termina", Filter{Props: []PropFilter{match("EMAIL", "@otra.test", MatchEndsWith, false)}}, false, true},
		{"negado", Filter{Props: []PropFilter{match("FN", "perez", MatchContains, true)}}, false, true},
		{"TEL no definido", Filter{Props: []PropFilter{{Name: "TEL", IsNotDefined: true}}}, false, true},
		{"TEL definido sin comparacion", Filter{Props: []PropFilter{{Name: "TEL"}}}, true, false},
		{"anyof", Filter{Props: []PropFilter{match("FN", "perez", MatchContains, false), match("FN", "ruiz", MatchContains, false)}}, true, true},
		{"allof", Filter{AllOf: true, Props: []PropFilter{match("FN", "ana", MatchContains, false), match("EMAIL", "acme", MatchContains, false)}}, true, false},
		{"octet distingue mayusculas", Filter{Props: []PropFilter{{Name: "FN", Matches: []TextMatch{{Text: "ANA", Collation: CollationOctet, Type: MatchContains}}}}}, false, false},
		{"alguna aparicion basta", Filter{Props: []PropFilter{match("EMAIL", "home", MatchContains, false)}}, false, false},
		{"segunda aparicion", Filter{Props: []PropFilter{match("EMAIL", "ana@acme.test", MatchEquals, false)}}, true, false},
		{"propiedad inexistente", Filter{Props: []PropFilter{match("ORG", "x", MatchContains, false)}}, false, false},
	}
	for _, c := range cases {
		if got := c.filter.Matches(ana); got != c.ana {
			t.Errorf("%s (ana): %v", c.name, got)
		}
		if got := c.filter.Matches(bea); got != c.bea {
			t.Errorf("%s (bea): %v", c.name, got)
		}
	}
}

func TestLimitesExigenValoresPositivos(t *testing.T) {
	if err := testLimits.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := testLimits
	bad.MaxChangesRetained = 0
	if bad.Validate() == nil {
		t.Fatal("un limite en cero debe rechazarse")
	}
}
