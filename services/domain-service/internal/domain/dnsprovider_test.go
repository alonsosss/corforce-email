package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const tokenDePrueba = "cf_Tok3n-de-prueba_0123456789abcdef"

func TestNewAPIToken(t *testing.T) {
	for nombre, c := range map[string]struct {
		in  string
		err bool
	}{
		"valido":                     {tokenDePrueba, false},
		"con espacios alrededor":     {"  " + tokenDePrueba + "\n", false},
		"vacio":                      {"", true},
		"corto":                      {"abc123", true},
		"largo":                      {strings.Repeat("a", 257), true},
		"con salto de linea dentro":  {"abcdefghij\r\nX-Inyectada: 1abcdefghij", true},
		"con espacio dentro":         {"abcdefghij abcdefghij", true},
		"con caracteres no ascii":    {"abcdefghijñabcdefghij", true},
		"con separador de cabeceras": {"abcdefghij:abcdefghij", true},
	} {
		tok, err := NewAPIToken(c.in)
		if c.err != errors.Is(err, ErrInvalidDNSProviderToken) {
			t.Errorf("%s: error %v", nombre, err)
		}
		if !c.err && tok.Reveal() != strings.TrimSpace(c.in) {
			t.Errorf("%s: valor %q", nombre, tok.Reveal())
		}
	}
}

// El token solo sale con Reveal: formateado, serializado o en un struct que se registra, sale oculto.
func TestAPITokenNoSeFiltra(t *testing.T) {
	tok, err := NewAPIToken(tokenDePrueba)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Hint() != "cdef" {
		t.Errorf("pista %q", tok.Hint())
	}
	envuelto := struct {
		Token APIToken
		Otro  string
	}{tok, "x"}
	js, _ := json.Marshal(envuelto)
	text, _ := tok.MarshalText()
	for nombre, salida := range map[string]string{
		"%v": fmt.Sprintf("%v", tok), "%s": fmt.Sprintf("%s", tok), "%+v": fmt.Sprintf("%+v", envuelto),
		"%#v": fmt.Sprintf("%#v", tok), "json": string(js), "texto": string(text),
		"error": fmt.Errorf("fallo con %v", tok).Error(),
	} {
		if strings.Contains(salida, tokenDePrueba) || strings.Contains(salida, "0123456789") {
			t.Errorf("%s filtra el token: %s", nombre, salida)
		}
	}
}

func TestDNSModes(t *testing.T) {
	if !DNSModeManual.Valid() || !DNSMode("cloudflare").Valid() || DNSMode("route53").Valid() || DNSMode("").Valid() {
		t.Error("modos validos")
	}
	if _, ok := DNSModeManual.Provider(); ok {
		t.Error("manual no es un proveedor")
	}
	d := &Domain{DNSMode: "cloudflare"}
	if !d.DNSAutomatic() || (&Domain{}).DNSAutomatic() {
		t.Error("DNSAutomatic")
	}
}

func TestZoneForDomain(t *testing.T) {
	zones := []DNSZone{{ID: "z1", Name: "acme.com"}, {ID: "z2", Name: "correo.acme.com"}, {ID: "z3", Name: "otroacme.com"}, {ID: "z4", Name: "com"}}
	for nombre, c := range map[string]struct {
		domain, zone string
		ok           bool
	}{
		"la zona es el dominio":           {"acme.com", "z1", true},
		"subdominio de una zona":          {"ventas.acme.com", "z1", true},
		"la zona mas especifica":          {"eu.correo.acme.com", "z2", true},
		"igual a la zona mas especifica":  {"correo.acme.com", "z2", true},
		"sufijo de texto: no es acme.com": {"xacme.com", "z4", true},
		"zona hermana no vale":            {"acme.org", "", false},
		"mayusculas y punto final":        {"Ventas.ACME.com.", "z1", true},
		"zona ajena con el mismo final":   {"otroacme.com", "z3", true},
		"sin zonas":                       {"nada.net", "", false},
	} {
		z, ok := ZoneForDomain(c.domain, zones)
		if ok != c.ok || z.ID != c.zone {
			t.Errorf("%s: %q -> %+v %v", nombre, c.domain, z, ok)
		}
	}
	if _, ok := ZoneForDomain("acme.com", []DNSZone{{ID: "z", Name: "correo.acme.com"}}); ok {
		t.Error("una zona hija no es zona del dominio padre")
	}
	if HostInZone("acme.com", "") || !HostInZone("_dmarc.acme.com", "acme.com") || HostInZone("acme.com.evil.net", "acme.com") {
		t.Error("HostInZone")
	}
}

func dominioDePrueba(purpose Purpose) *Domain {
	d := fixture(purpose)
	d.Domain = "acme.com"
	return d
}

var plataformaDePrueba = PlatformDNS{MXHostname: "MX.plataforma.example.", SPFInclude: "include:spf.plataforma.example", DMARCRUA: "dmarc@plataforma.example"}

func TestDesiredRecords(t *testing.T) {
	records := DesiredRecords(dominioDePrueba(PurposeCorporate), plataformaDePrueba, "")
	kinds := make([]string, 0, len(records))
	for _, r := range records {
		kinds = append(kinds, string(r.Kind))
		if r.Kind == RecordMX && (r.Content != "mx.plataforma.example" || r.Priority != 10 || r.Name != "acme.com") {
			t.Errorf("MX %+v", r)
		}
	}
	if got := strings.Join(kinds, ","); got != "ownership_txt,mx,spf,dkim,dmarc" {
		t.Errorf("registros %s", got)
	}
	if len(DesiredRecords(dominioDePrueba(PurposeSending), plataformaDePrueba, "")) != 4 {
		t.Error("un dominio de envio no lleva MX")
	}
	d := dominioDePrueba(PurposeBoth)
	now := time.Now()
	d.DKIMPreviousSelector, d.DKIMPreviousPrivateKeyEnc, d.DKIMPreviousPublicKey, d.DKIMRotatedAt = "cfm202608", []byte{1}, "PREV", &now
	if n := len(DesiredRecords(d, plataformaDePrueba, "")); n != 6 {
		t.Errorf("con clave en gracia %d registros", n)
	}
}

func desired(kind RecordKind) DesiredRecord {
	for _, r := range DesiredRecords(dominioDePrueba(PurposeCorporate), plataformaDePrueba, "") {
		if r.Kind == kind {
			return r
		}
	}
	panic(kind)
}

func TestPlanRecord(t *testing.T) {
	spf := desired(RecordSPF)
	mx := desired(RecordMX)
	dkim := desired(RecordDKIM)
	own := desired(RecordOwnershipTXT)
	nuestro := func(id, content string) ProviderRecord {
		return ProviderRecord{ID: id, Type: "TXT", Name: "acme.com", Content: content, Comment: ManagedRecordComment}
	}
	suyo := func(id, content string) ProviderRecord {
		return ProviderRecord{ID: id, Type: "TXT", Name: "acme.com", Content: content, Comment: "lo puso el cliente"}
	}
	verificacion := ProviderRecord{ID: "g", Type: "TXT", Name: "acme.com", Content: "google-site-verification=abc"}

	for nombre, c := range map[string]struct {
		want     DesiredRecord
		existing []ProviderRecord
		replace  bool
		action   RecordAction
		create   bool
		update   string
		deletes  int
		foreign  int
	}{
		"nada publicado: se crea":                               {spf, nil, false, RecordCreated, true, "", 0, 0},
		"identico del cliente: no se toca":                      {spf, []ProviderRecord{suyo("a", spf.Content)}, false, RecordUnchanged, false, "", 0, 0},
		"identico con comillas: no se toca":                     {spf, []ProviderRecord{suyo("a", `"`+spf.Content+`"`)}, false, RecordUnchanged, false, "", 0, 0},
		"otros TXT del nombre no cuentan":                       {spf, []ProviderRecord{verificacion}, false, RecordCreated, true, "", 0, 0},
		"SPF del cliente distinto: conflicto":                   {spf, []ProviderRecord{suyo("a", "v=spf1 include:_spf.google.com ~all")}, false, RecordConflict, false, "", 0, 1},
		"SPF del cliente distinto confirmado: se pisa":          {spf, []ProviderRecord{suyo("a", "v=spf1 include:_spf.google.com ~all")}, true, RecordReplaced, false, "a", 0, 1},
		"nuestro identico sin comillas: se reescribe con ellas": {spf, []ProviderRecord{nuestro("a", spf.Content)}, false, RecordUpdated, false, "a", 0, 0},
		"nuestro identico con comillas: no se toca":             {spf, []ProviderRecord{nuestro("a", `"`+spf.Content+`"`)}, false, RecordUnchanged, false, "", 0, 0},
		"DKIM nuestro sin comillas: se reescribe con ellas":     {dkim, []ProviderRecord{{ID: "d", Type: "TXT", Name: dkim.Name, Content: dkim.Content, Comment: ManagedRecordComment}}, false, RecordUpdated, false, "d", 0, 0},
		"DKIM nuestro partido en trozos: no se toca":            {dkim, []ProviderRecord{{ID: "d", Type: "TXT", Name: dkim.Name, Content: `"` + dkim.Content[:5] + `" "` + dkim.Content[5:] + `"`, Comment: ManagedRecordComment}}, false, RecordUnchanged, false, "", 0, 0},
		"SPF nuestro distinto: se actualiza sin pedir":          {spf, []ProviderRecord{nuestro("a", "v=spf1 include:viejo -all")}, false, RecordUpdated, false, "a", 0, 0},
		"identico mas otro SPF del cliente: conflicto":          {spf, []ProviderRecord{nuestro("a", spf.Content), suyo("b", "v=spf1 -all")}, false, RecordConflict, false, "", 0, 1},
		"identico mas otro SPF confirmado: se retira":           {spf, []ProviderRecord{nuestro("a", spf.Content), suyo("b", "v=spf1 -all")}, true, RecordReplaced, false, "", 1, 1},
		"dos SPF nuestros: uno se actualiza y otro sale":        {spf, []ProviderRecord{nuestro("a", "v=spf1 a -all"), nuestro("b", "v=spf1 b -all")}, false, RecordUpdated, false, "a", 1, 0},
		"SPF en mayusculas del cliente: conflicto":              {spf, []ProviderRecord{suyo("a", "V=SPF1 mx -all")}, false, RecordConflict, false, "", 0, 1},
		"v=spf10 no es SPF":                                     {spf, []ProviderRecord{suyo("a", "v=spf10 raro")}, false, RecordCreated, true, "", 0, 0},
		"MX del cliente: conflicto":                             {mx, []ProviderRecord{{ID: "m", Type: "MX", Name: "acme.com", Content: "aspmx.l.google.com", Priority: 1}}, false, RecordConflict, false, "", 0, 1},
		"MX igual con otra prioridad: conflicto":                {mx, []ProviderRecord{{ID: "m", Type: "MX", Name: "acme.com", Content: "mx.plataforma.example", Priority: 20}}, false, RecordConflict, false, "", 0, 1},
		"MX igual con punto final: no se toca":                  {mx, []ProviderRecord{{ID: "m", Type: "MX", Name: "ACME.com.", Content: "mx.plataforma.example.", Priority: 10}}, false, RecordUnchanged, false, "", 0, 0},
		"DKIM partido en trozos: no se toca":                    {dkim, []ProviderRecord{{ID: "d", Type: "TXT", Name: dkim.Name, Content: `"` + dkim.Content[:5] + `" "` + dkim.Content[5:] + `"`}}, false, RecordUnchanged, false, "", 0, 0},
		"propiedad antigua nuestra: se actualiza":               {own, []ProviderRecord{{ID: "o", Type: "TXT", Name: own.Name, Content: "cfm-verify=viejo", Comment: ManagedRecordComment}}, false, RecordUpdated, false, "o", 0, 0},
		"registro de otro nombre no cuenta":                     {spf, []ProviderRecord{{ID: "x", Type: "TXT", Name: "otro.acme.com", Content: "v=spf1 -all"}}, false, RecordCreated, true, "", 0, 0},
	} {
		plan := PlanRecord(c.want, c.existing, c.replace)
		update := ""
		if plan.Update != nil {
			update = plan.Update.ID
		}
		if plan.Action != c.action || plan.Create != c.create || update != c.update || len(plan.Delete) != c.deletes || len(plan.Foreign) != c.foreign {
			t.Errorf("%s: %s create=%v update=%q delete=%d foreign=%d", nombre, plan.Action, plan.Create, update, len(plan.Delete), len(plan.Foreign))
		}
	}
}

func TestQuoteTXT(t *testing.T) {
	larga := strings.Repeat("abcdefghij", 39) // 390 caracteres: una clave DKIM de 2048 bits pasa de 255
	for nombre, in := range map[string]string{
		"corto":             "v=spf1 include:spf.plataforma.example -all",
		"vacio":             "",
		"con comillas":      `con "comillas" dentro`,
		"con barra":         `ruta\con\barra`,
		"clave larga":       larga,
		"justo en el corte": strings.Repeat("x", 255),
		"un caracter mas":   strings.Repeat("x", 256),
		"con espacios":      "  con espacios  ",
	} {
		got := QuoteTXT(in)
		if !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
			t.Errorf("%s: sin comillas: %q", nombre, got)
		}
		if want := strings.TrimSpace(in); normalizeTXT(got) != want {
			t.Errorf("%s: no vuelve al valor: %q", nombre, normalizeTXT(got))
		}
		for _, trozo := range strings.Split(strings.ReplaceAll(got, `" "`, "\x00"), "\x00") {
			if n := len(strings.Trim(trozo, `"`)); n > 2*txtChunk { // el escape puede duplicar el largo
				t.Errorf("%s: trozo de %d caracteres", nombre, n)
			}
		}
	}
	if got := QuoteTXT(larga); strings.Count(got, `" "`) != 1 {
		t.Errorf("390 caracteres deben ir en dos cadenas: %q", got)
	}
	if got := QuoteTXT(strings.Repeat("x", 255)); strings.Contains(got, `" "`) {
		t.Errorf("255 caracteres caben en una sola cadena: %q", got)
	}
	ya := `"ya entre comillas"`
	if got := QuoteTXT(ya); got != ya {
		t.Errorf("un valor con comillas se deja igual: %q", got)
	}
}

func TestNormalizeTXT(t *testing.T) {
	for in, want := range map[string]string{
		`v=spf1 -all`:        "v=spf1 -all",
		`"v=spf1 -all"`:      "v=spf1 -all",
		`"abc" "def"`:        "abcdef",
		`"abc""def"`:         "abcdef",
		`"con \"comillas\""`: `con "comillas"`,
		`  "espacios"  `:     "espacios",
	} {
		if got := normalizeTXT(in); got != want {
			t.Errorf("normalizeTXT(%q) = %q", in, got)
		}
	}
}

func TestDNSPublication(t *testing.T) {
	p := &DNSPublication{Records: []RecordResult{{Action: RecordCreated}, {Action: RecordUnchanged}, {Action: RecordUnchanged}}}
	if !p.Complete() || p.Count(RecordUnchanged) != 2 {
		t.Error("completa")
	}
	p.Records = append(p.Records, RecordResult{Action: RecordConflict})
	if p.Complete() {
		t.Error("con conflicto no esta completa")
	}
	if got := ExistingValue(ProviderRecord{Type: "MX", Content: "Aspmx.Google.com.", Priority: 5}); got != "aspmx.google.com priority 5" {
		t.Errorf("valor MX %q", got)
	}
	c := &DNSProviderConnection{}
	zones := make([]DNSZone, MaxStoredZones+3)
	for i := range zones {
		zones[i] = DNSZone{ID: "z", Name: fmt.Sprintf("z%d.com", i)}
	}
	at := time.Now()
	c.SetZones(zones, at)
	if len(c.Zones) != MaxStoredZones || c.ZonesVisible != MaxStoredZones+3 || !c.LastValidatedAt.Equal(at) {
		t.Errorf("zonas %d visibles %d", len(c.Zones), c.ZonesVisible)
	}
}
