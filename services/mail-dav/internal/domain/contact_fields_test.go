package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var stamp = time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

var fieldLimits = Limits{MaxVCardBytes: 64 << 10, MaxVCardProperties: 200, MaxContactsPerMailbox: 10, MaxAddressbooksPerMailbox: 3, MaxChangesRetained: 5, MaxMailboxBytes: 1 << 20, MaxReadBytes: 1 << 20}

func fullContact() ContactFields {
	return ContactFields{
		Name: "Ana Pérez; Jr, \\ Hija", GivenName: "Ana", FamilyName: "Pérez",
		Emails:       []TypedValue{{Value: "ana@acme.test", Type: "work"}, {Value: "ana@casa.test", Type: "home"}},
		Phones:       []TypedValue{{Value: "+51 999 888 777", Type: "mobile"}, {Value: "01-555", Type: "other"}},
		Organization: "Acme, S.A.", Title: "Gerente", Notes: "Linea uno\nLinea dos; con, signos", Birthday: "1985-04-12",
	}
}

func mustBuildCard(t *testing.T, existing string, f ContactFields) string {
	t.Helper()
	n, err := f.Normalize()
	if err != nil {
		t.Fatalf("normalizar: %v", err)
	}
	raw, err := BuildVCard(existing, "8f2d7c1e-uid", n, stamp)
	if err != nil {
		t.Fatalf("escribir: %v", err)
	}
	if _, err := ParseVCard(raw, fieldLimits); err != nil {
		t.Fatalf("el vCard generado no pasa ParseVCard: %v\n%s", err, raw)
	}
	return raw
}

func TestContactoIdaYVuelta(t *testing.T) {
	in := fullContact()
	raw := mustBuildCard(t, "", in)
	if !strings.HasPrefix(raw, "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:8f2d7c1e-uid\r\n") || !strings.Contains(raw, "REV:20260924T100000Z") {
		t.Fatalf("cabecera del vCard:\n%s", raw)
	}
	for _, want := range []string{`FN:Ana Pérez\; Jr\, \\ Hija`, `ORG:Acme\, S.A.`, `NOTE:Linea uno\nLinea dos\; con\, signos`, "BDAY:19850412",
		"EMAIL;TYPE=work:ana@acme.test", "TEL;VALUE=text;TYPE=cell:+51 999 888 777", "TEL;VALUE=text:01-555", `N:Pérez;Ana;;;`} {
		if !strings.Contains(raw, want) {
			t.Errorf("falta %q en\n%s", want, raw)
		}
	}
	got, err := ContactFieldsOf(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("ida y vuelta:\nquiero %+v\ntengo  %+v", in, got)
	}
}

func TestContactoSinAnioDeNacimientoYSinNombreExplicito(t *testing.T) {
	raw := mustBuildCard(t, "", ContactFields{GivenName: "Luis", FamilyName: "Rojas", Birthday: "--04-12"})
	got, err := ContactFieldsOf(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Luis Rojas" || got.Birthday != "--04-12" || !strings.Contains(raw, "BDAY:--0412") {
		t.Fatalf("nombre derivado y cumpleanos sin anio: %+v\n%s", got, raw)
	}
	raw = mustBuildCard(t, "", ContactFields{Emails: []TypedValue{{Value: "solo@correo.test"}}})
	if got, _ := ContactFieldsOf(raw); got.Name != "solo@correo.test" || got.Emails[0].Type != ContactTypeOther {
		t.Fatalf("solo correo: %+v", got)
	}
}

// Un salto de linea en un campo nunca abre una propiedad nueva: sale escapado o se rechaza.
func TestContactoNoAdmiteInyeccionDeLineas(t *testing.T) {
	for name, f := range map[string]ContactFields{
		"nombre":   {Name: "Ana\r\nEMAIL:intruso@x.test"},
		"cargo":    {Name: "Ana", Title: "Jefe\nX-EVIL:1"},
		"correo":   {Name: "Ana", Emails: []TypedValue{{Value: "a@b.test\r\nTEL:1"}}},
		"telefono": {Name: "Ana", Phones: []TypedValue{{Value: "1\n2"}}},
		"empresa":  {Name: "Ana", Organization: "A\x00B"},
	} {
		var fe *FieldError
		if _, err := f.Normalize(); !errors.As(err, &fe) {
			t.Errorf("%s: debia rechazarse, error %v", name, err)
		}
	}
	raw := mustBuildCard(t, "", ContactFields{Name: "Ana", Notes: "uno\r\nEMAIL:intruso@x.test\nEND:VCARD"})
	card, err := ParseVCard(raw, fieldLimits)
	if err != nil {
		t.Fatal(err)
	}
	if emails := card.Emails(); len(emails) != 0 {
		t.Fatalf("la nota abrio una propiedad: %v\n%s", emails, raw)
	}
	if got, _ := ContactFieldsOf(raw); got.Notes != "uno\nEMAIL:intruso@x.test\nEND:VCARD" {
		t.Fatalf("la nota no vuelve igual: %q", got.Notes)
	}
}

func TestContactoValidaLosCampos(t *testing.T) {
	cases := map[string]struct {
		in    ContactFields
		field string
	}{
		"vacio":          {ContactFields{}, "name"},
		"correo malo":    {ContactFields{Emails: []TypedValue{{Value: "no-es-correo"}}}, "emails[0].value"},
		"tipo raro":      {ContactFields{Name: "a", Phones: []TypedValue{{Value: "1", Type: "fax"}}}, "phones[0].type"},
		"telefono vacio": {ContactFields{Name: "a", Phones: []TypedValue{{Value: " "}}}, "phones[0].value"},
		"cumpleanos":     {ContactFields{Name: "a", Birthday: "12/04/1985"}, "birthday"},
		"fecha invalida": {ContactFields{Name: "a", Birthday: "1985-02-30"}, "birthday"},
		"nombre largo":   {ContactFields{Name: strings.Repeat("a", MaxContactTextRunes+1)}, "name"},
		"notas largas":   {ContactFields{Name: "a", Notes: strings.Repeat("a", MaxContactNotesRunes+1)}, "notes"},
		"muchos correos": {ContactFields{Name: "a", Emails: make([]TypedValue, MaxContactEmails+1)}, "emails"},
	}
	for name, c := range cases {
		var fe *FieldError
		if _, err := c.in.Normalize(); !errors.As(err, &fe) || fe.Field != c.field {
			t.Errorf("%s: quiero error en %q, tengo %v", name, c.field, err)
		}
	}
}

// Actualizar desde la API conserva lo que la API no expresa: la version, el UID, la foto, las extensiones,
// los grupos de etiquetas y los parametros de cada valor que no cambio.
func TestContactoConservaLoQueNoConoce(t *testing.T) {
	existing := vcardWith(
		"VERSION:3.0", "PRODID:-//Apple Inc.//iPhone OS 17.0//EN", "N:Pérez;Ana;María;Dra.;", "FN:Ana Pérez", "ORG:Acme;Ventas",
		"item1.EMAIL;type=INTERNET;type=WORK;type=pref:ana@acme.test", "item1.X-ABLabel:Oficina",
		"item2.EMAIL;type=INTERNET:vieja@acme.test", "item2.X-ABLabel:Antigua",
		"TEL;type=CELL;type=VOICE;type=pref:+51 999", "PHOTO;ENCODING=b;TYPE=JPEG:QUJD", "X-SOCIALPROFILE;type=twitter:x.com/ana",
		"UID:uid-del-iphone", "REV:2020-01-01T00:00:00Z",
	)
	cur, err := ContactFieldsOf(existing)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Organization != "Acme" || cur.Emails[0] != (TypedValue{"ana@acme.test", ContactTypeWork}) || cur.Phones[0].Type != ContactTypeMobile {
		t.Fatalf("lectura de una tarjeta de iOS: %+v", cur)
	}
	upd := cur
	upd.Emails = []TypedValue{cur.Emails[0], {Value: "nueva@acme.test", Type: "home"}}
	upd.Title = "Directora"
	raw := mustBuildCard(t, existing, upd)
	for _, want := range []string{"VERSION:3.0", "UID:uid-del-iphone", "PHOTO;ENCODING=b;TYPE=JPEG:QUJD", "X-SOCIALPROFILE;type=twitter:x.com/ana",
		"PRODID:-//Apple Inc.//iPhone OS 17.0//EN", "item1.EMAIL;type=INTERNET;type=WORK;type=pref:ana@acme.test", "item1.X-ABLabel:Oficina",
		"N:Pérez;Ana;María;Dra.;", "ORG:Acme;Ventas", "TEL;type=CELL;type=VOICE;type=pref:+51 999", "EMAIL;TYPE=HOME:nueva@acme.test", "TITLE:Directora",
		"REV:20260924T100000Z"} {
		if !strings.Contains(raw, want) {
			t.Errorf("falta %q en\n%s", want, raw)
		}
	}
	for _, gone := range []string{"vieja@acme.test", "item2.X-ABLabel", "REV:2020"} {
		if strings.Contains(raw, gone) {
			t.Errorf("sobra %q en\n%s", gone, raw)
		}
	}
	got, _ := ContactFieldsOf(raw)
	if !reflect.DeepEqual(got, upd) {
		t.Fatalf("despues de actualizar:\nquiero %+v\ntengo  %+v", upd, got)
	}

	// Cambiar el apellido conserva el segundo nombre y el prefijo.
	upd.FamilyName = "Gómez"
	raw = mustBuildCard(t, raw, upd)
	if !strings.Contains(raw, "N:Gómez;Ana;María;Dra.;") {
		t.Fatalf("N parcial:\n%s", raw)
	}
}

func TestLineasLargasSePlieganA75Octetos(t *testing.T) {
	notes := strings.Repeat("ñandú áéíóú ", 60)
	raw := mustBuildCard(t, "", ContactFields{Name: "Ana", Notes: notes})
	for _, line := range strings.Split(strings.TrimSuffix(raw, "\r\n"), "\r\n") {
		if len(line) > 75 {
			t.Fatalf("linea de %d octetos: %q", len(line), line)
		}
		if !utf8.ValidString(line) {
			t.Fatalf("el plegado partio un caracter: %q", line)
		}
	}
	got, _ := ContactFieldsOf(raw)
	if got.Notes != strings.TrimSpace(notes) {
		t.Fatal("la nota plegada no vuelve igual")
	}
}

func TestSplitVCardsYEnsureUID(t *testing.T) {
	file := "\uFEFFBEGIN:VCARD\nVERSION:3.0\nFN:Uno\nEND:VCARD\n\nbasura suelta\nBEGIN:VCARD\r\nVERSION:4.0\r\nUID:dos\r\nFN:Dos\r\nEND:VCARD\r\nBEGIN:VCARD\nVERSION:4.0\nFN:sin cerrar\n"
	cards, err := SplitVCards(file, 10)
	if err != nil || len(cards) != 3 {
		t.Fatalf("tarjetas: %d %v", len(cards), err)
	}
	first := EnsureUID(cards[0], "uid-nuevo")
	if c, err := ParseVCard(first, fieldLimits); err != nil || c.UID != "uid-nuevo" {
		t.Fatalf("sin UID se le anade uno: %v %+v\n%q", err, c, first)
	}
	if EnsureUID(cards[1], "otro") != cards[1] {
		t.Fatal("con UID no se toca")
	}
	if _, err := ParseVCard(EnsureUID(cards[2], "x"), fieldLimits); err == nil {
		t.Fatal("una tarjeta sin cerrar no es valida")
	}
	if _, err := SplitVCards(file, 2); !errors.Is(err, ErrImportTooManyCards) {
		t.Fatalf("tope de tarjetas: %v", err)
	}
}
