package domain

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestClassifySigueElOrdenDeLasReglas(t *testing.T) {
	person := []Address{{Name: "Luis", Email: "luis@cliente.test"}}
	cases := []struct {
		name string
		h    MessageHeaders
		from []Address
		want Category
	}{
		{"persona", MessageHeaders{}, person, CategoryPrimary},
		{"respuesta humana declarada", MessageHeaders{HeaderAutoSubmitted: {"no"}}, person, CategoryPrimary},
		{"auto-notified no es no", MessageHeaders{HeaderAutoSubmitted: {"auto-notified"}}, person, CategoryNotifications},
		{"generado con baja sigue siendo notificacion", MessageHeaders{HeaderAutoSubmitted: {"Auto-Generated"}, HeaderListUnsubscribe: {"<https://x.test/u>"}}, person, CategoryNotifications},
		{"boletin", MessageHeaders{HeaderListUnsubscribe: {"<mailto:baja@x.test>"}}, person, CategoryNewsletters},
		{"lista", MessageHeaders{HeaderListID: {"<equipo.lista.test>"}}, person, CategoryNewsletters},
		{"precedence list", MessageHeaders{HeaderPrecedence: {"list"}}, person, CategoryNewsletters},
		{"masivo sin baja", MessageHeaders{HeaderPrecedence: {"Bulk"}}, person, CategoryNotifications},
		{"masivo con baja", MessageHeaders{HeaderPrecedence: {"bulk"}, HeaderListUnsubscribe: {"<https://x.test/u>"}}, person, CategoryNewsletters},
		{"noreply en la direccion", MessageHeaders{}, []Address{{Email: "No-Reply@banco.test"}}, CategoryNotifications},
		{"noreply en el nombre", MessageHeaders{}, []Address{{Name: "Banco (noreply)", Email: "avisos@banco.test"}}, CategoryNotifications},
		{"notificaciones", MessageHeaders{}, []Address{{Name: "GitHub", Email: "notifications@github.test"}}, CategoryNotifications},
	}
	for _, c := range cases {
		if got := Classify(c.h, c.from); got != c.want {
			t.Errorf("%s: %q, quiero %q", c.name, got, c.want)
		}
	}
}

func TestParseCategoryYVista(t *testing.T) {
	for _, c := range Categories {
		if got, err := ParseCategory(string(c)); err != nil || got != c {
			t.Errorf("%s: %v", c, err)
		}
	}
	if got, err := ParseCategory(""); err != nil || got != "" {
		t.Fatal("vacio es sin filtro")
	}
	var verr *ValidationError
	if _, err := ParseCategory("promociones"); !errors.As(err, &verr) || verr.Field != "category" {
		t.Fatalf("err=%v", err)
	}
	if v, err := ParseListView(""); err != nil || v != ViewMessages {
		t.Fatal("vacio es por mensajes")
	}
	if v, err := ParseListView("threads"); err != nil || v != ViewThreads {
		t.Fatal("threads")
	}
	if _, err := ParseListView("hilos"); !errors.As(err, &verr) || verr.Field != "view" {
		t.Fatalf("err=%v", err)
	}
}

func TestParseUnsubscribePrefiereElClicDeclarado(t *testing.T) {
	both := MessageHeaders{
		HeaderListUnsubscribe:     {"<mailto:baja@tienda.test?subject=baja>, <https://tienda.test/u/abc#x>"},
		HeaderListUnsubscribePost: {"List-Unsubscribe=One-Click"},
	}
	u := ParseUnsubscribe(both)
	if u.Method != UnsubscribeOneClick || u.URL != "https://tienda.test/u/abc" || u.Host() != "tienda.test" {
		t.Fatalf("un clic: %+v", u)
	}
	delete(both, HeaderListUnsubscribePost)
	u = ParseUnsubscribe(both)
	if u.Method != UnsubscribeMailto || u.Mailto.Address.Email != "baja@tienda.test" || u.Mailto.Subject != "baja" {
		t.Fatalf("sin POST se prefiere el correo: %+v", u)
	}
	u = ParseUnsubscribe(MessageHeaders{HeaderListUnsubscribe: {"<https://tienda.test/u>"}})
	if u.Method != UnsubscribeWeb || u.URL != "https://tienda.test/u" {
		t.Fatalf("solo pagina: %+v", u)
	}
	if u := ParseUnsubscribe(MessageHeaders{}); u.Method != "" || u.Host() != "" {
		t.Fatalf("sin cabecera: %+v", u)
	}
}

func TestParseUnsubscribeDescartaLoQueNoSeAdmite(t *testing.T) {
	cases := map[string]string{
		"http sin TLS":              "<http://tienda.test/u>",
		"otro puerto":               "<https://tienda.test:8443/u>",
		"credenciales":              "<https://user:pass@tienda.test/u>",
		"javascript":                "<javascript:alert(1)>",
		"mailto con varios":         "<mailto:a@x.test,b@x.test>",
		"mailto invalido":           "<mailto:no es correo>",
		"sin corchetes":             "https://tienda.test/u",
		"URL gigante":               "<https://tienda.test/" + strings.Repeat("a", MaxUnsubscribeURLBytes) + ">",
		"mailto con salto de linea": "<mailto:baja@x.test%0D%0ABcc:otro@x.test>",
	}
	for name, header := range cases {
		h := MessageHeaders{HeaderListUnsubscribe: {header}, HeaderListUnsubscribePost: {"List-Unsubscribe=One-Click"}}
		if u := ParseUnsubscribe(h); u.Method != "" {
			t.Errorf("%s: se acepto %+v", name, u)
		}
	}
}

func TestUnsubscribeAllowedIn(t *testing.T) {
	for _, role := range []FolderRole{RoleSent, RoleDrafts, RoleScheduled, RoleJunk} {
		if UnsubscribeAllowedIn(role) {
			t.Errorf("%s no ofrece baja", role)
		}
	}
	for _, role := range []FolderRole{RoleInbox, RoleArchive, RoleTrash, ""} {
		if !UnsubscribeAllowedIn(role) {
			t.Errorf("%q ofrece baja", role)
		}
	}
}

func TestMailtoAcotaElAsuntoYElCuerpo(t *testing.T) {
	h := MessageHeaders{HeaderListUnsubscribe: {"<mailto:baja@x.test?subject=" + strings.Repeat("s", 300) + "%0D%0AX-Evil:%201&body=linea%0Aotra%00" + strings.Repeat("b", 2000) + ">"}}
	u := ParseUnsubscribe(h)
	if u.Mailto == nil {
		t.Fatal("mailto")
	}
	if len([]rune(u.Mailto.Subject)) > 200 || strings.ContainsAny(u.Mailto.Subject, "\r\n") {
		t.Fatalf("asunto: %q", u.Mailto.Subject)
	}
	if len([]rune(u.Mailto.Body)) > 1000 || !strings.Contains(u.Mailto.Body, "linea\notra") || strings.ContainsRune(u.Mailto.Body, 0) {
		t.Fatalf("cuerpo: %q", u.Mailto.Body[:20])
	}
	if u := ParseUnsubscribe(MessageHeaders{HeaderListUnsubscribe: {"<mailto:baja@x.test>"}}); u.Mailto.Subject != defaultMailtoSubject {
		t.Fatalf("asunto por defecto: %+v", u.Mailto)
	}
}

func TestParseAuthenticationSeQuedaConElPeor(t *testing.T) {
	h := MessageHeaders{
		HeaderAuthResults: {
			"falso.test; spf=pass; dkim=pass; dmarc=pass",
			"rspamd; dkim=fail (firma rota) header.d=x.test; dkim=pass header.d=y.test; spf=softfail smtp.mailfrom=x.test",
		},
		HeaderSpamdResult: {"default: False [4.00 / 15.00]; DMARC_POLICY_QUARANTINE(1.50)[x.test : No valid SPF, quarantine]; R_SPF_SOFTFAIL(0.00)[~all]"},
	}
	got := ParseAuthentication(h)
	if got.SPF != AuthSoftFail || got.DKIM != AuthPass || got.DMARC != AuthFail {
		t.Fatalf("%+v", got)
	}
	if got := ParseAuthentication(MessageHeaders{}); got != (AuthResults{}) {
		t.Fatalf("sin cabeceras no hay resultado: %+v", got)
	}
	if got := ParseAuthentication(MessageHeaders{HeaderAuthResults: {"mx; spf/v=unknownvalue; iprev=pass"}}); got.SPF != "" {
		t.Fatalf("un resultado desconocido no cuenta: %+v", got)
	}
}

func assess(from Address, h MessageHeaders, colleagues ...AddressBookEntry) Shield {
	return AssessSender(ShieldInput{
		From:       []Address{from},
		Headers:    h,
		OwnDomains: []string{"empresa.pe", "Empresa-Holding.com."},
		Colleagues: colleagues,
	})
}

func reasons(s Shield) []string {
	var out []string
	for _, r := range s.Reasons {
		out = append(out, r.Code)
	}
	return out
}

func TestEscudoInternoLimpioYSuplantacionDelDominioPropio(t *testing.T) {
	s := assess(Address{Name: "Luis", Email: "luis@empresa.pe"}, MessageHeaders{})
	if s.Level != ShieldNone || s.External || len(s.Reasons) != 0 {
		t.Fatalf("interno sin resultados (envio autenticado): %+v", s)
	}
	s = assess(Address{Name: "Luis", Email: "luis@ventas.empresa.pe"}, MessageHeaders{})
	if s.External {
		t.Fatal("un subdominio propio es interno")
	}
	s = assess(Address{Name: "Gerencia", Email: "gerencia@empresa.pe"}, MessageHeaders{HeaderAuthResults: {"mx; spf=fail; dmarc=fail"}})
	if s.Level != ShieldDanger || !slices.Contains(reasons(s), ReasonOwnDomainSpoof) {
		t.Fatalf("el propio dominio sin verificar es un fraude: %+v", s)
	}
}

func TestEscudoExternoFallosDeAutenticacion(t *testing.T) {
	s := assess(Address{Name: "Proveedor", Email: "cobros@proveedor.test"}, MessageHeaders{HeaderAuthResults: {"mx; spf=pass; dkim=pass; dmarc=pass"}})
	if s.Level != ShieldInfo || !s.External || !slices.Equal(reasons(s), []string{ReasonExternalSender}) {
		t.Fatalf("externo verificado: %+v", s)
	}
	s = assess(Address{Email: "cobros@proveedor.test"}, MessageHeaders{HeaderAuthResults: {"mx; spf=softfail; dkim=fail"}})
	if s.Level != ShieldCaution || !slices.Contains(reasons(s), ReasonSPFFail) || !slices.Contains(reasons(s), ReasonDKIMFail) {
		t.Fatalf("fallos sin DMARC: %+v", s)
	}
	s = assess(Address{Email: "cobros@proveedor.test"}, MessageHeaders{HeaderSpamdResult: {"default: True; DMARC_POLICY_REJECT(2.0)"}})
	if s.Level != ShieldDanger || !slices.Contains(reasons(s), ReasonDMARCFail) {
		t.Fatalf("DMARC: %+v", s)
	}
	if s.Auth.DMARC != AuthFail {
		t.Fatalf("auth: %+v", s.Auth)
	}
}

func TestEscudoDominiosParecidos(t *testing.T) {
	cases := []struct {
		domain string
		want   string
	}{
		{"ernpresa.pe", ReasonHomoglyphDomain},
		{"empresa.pe.cobros.io", ReasonLookalikeDomain},
		{"empressa.pe", ReasonLookalikeDomain},
		{"empresa.pa", ReasonLookalikeDomain},
		{"xn--mpresa-2of.pe", ReasonHomoglyphDomain},
		{"empresa-holding.co", ReasonLookalikeDomain},
		{"empresa-ho1ding.com", ReasonHomoglyphDomain},
		{"proveedor.test", ""},
		{"gmail.com", ""},
	}
	for _, c := range cases {
		s := assess(Address{Name: "Pagos", Email: "pagos@" + c.domain}, MessageHeaders{})
		got := ""
		for _, r := range s.Reasons {
			if r.Code == ReasonHomoglyphDomain || r.Code == ReasonLookalikeDomain {
				got = r.Code
				if r.Level != ShieldDanger || r.Params["domain"] != c.domain || r.Params["resembles"] == "" {
					t.Errorf("%s: motivo mal formado %+v", c.domain, r)
				}
			}
		}
		if got != c.want {
			t.Errorf("%s: %q, quiero %q (%v)", c.domain, got, c.want, reasons(s))
		}
	}
}

func TestLookalikeKindCasosLimite(t *testing.T) {
	if LookalikeKind("", "empresa.pe") != "" || LookalikeKind("empresa.pe", "") != "" || LookalikeKind("empresa.pe", "empresa.pe") != "" {
		t.Fatal("vacios e iguales no se comparan")
	}
	if LookalikeKind("mail.empresa.pe", "empresa.pe") != "" {
		t.Fatal("un subdominio propio no es un parecido")
	}
	if got := LookalikeKind("еmpresa.pe", "empresa.pe"); got != ReasonHomoglyphDomain {
		t.Fatalf("cirilico sin codificar: %q", got)
	}
	if editDistance("abc", "abcdef", 1) != 2 || editDistance("kitten", "sitting", 5) != 3 {
		t.Fatal("distancia")
	}
}

func TestEscudoSuplantacionDeUnCompanero(t *testing.T) {
	carlos := AddressBookEntry{Address: "carlos.ruiz@empresa.pe", DisplayName: "Carlos Ruíz"}
	s := assess(Address{Name: "carlos  ruiz", Email: "ceo.carlos@gratis.test"}, MessageHeaders{}, carlos)
	if s.Level != ShieldDanger || !slices.Contains(reasons(s), ReasonColleagueName) {
		t.Fatalf("nombre de un companero desde fuera: %+v", s)
	}
	for _, r := range s.Reasons {
		if r.Code == ReasonColleagueName && r.Params["address"] != "carlos.ruiz@empresa.pe" {
			t.Fatalf("params: %+v", r.Params)
		}
	}
	s = assess(Address{Name: "Carlos.Ruiz", Email: "x@gratis.test"}, MessageHeaders{}, AddressBookEntry{Address: "carlos.ruiz@empresa.pe"})
	if !slices.Contains(reasons(s), ReasonColleagueName) {
		t.Fatalf("la parte local del companero como nombre: %+v", s)
	}
	s = assess(Address{Name: "Cl", Email: "x@gratis.test"}, MessageHeaders{}, AddressBookEntry{Address: "cl@empresa.pe", DisplayName: "Cl"})
	if slices.Contains(reasons(s), ReasonColleagueName) {
		t.Fatal("un nombre demasiado corto no se compara")
	}
	s = assess(Address{Name: "Carlos Ruiz", Email: "carlos.ruiz@empresa.pe"}, MessageHeaders{}, carlos)
	if s.Level != ShieldNone {
		t.Fatalf("el propio companero no se suplanta a si mismo: %+v", s)
	}
}

func TestEscudoDireccionDentroDelNombreYReplyTo(t *testing.T) {
	s := assess(Address{Name: "gerencia@empresa.pe", Email: "x@gratis.test"}, MessageHeaders{})
	found := false
	for _, r := range s.Reasons {
		if r.Code == ReasonEmbeddedAddress {
			found = r.Level == ShieldDanger && r.Params["address"] == "gerencia@empresa.pe"
		}
	}
	if !found || s.Level != ShieldDanger {
		t.Fatalf("direccion propia en el nombre: %+v", s)
	}
	s = assess(Address{Name: "soporte@banco.test", Email: "x@gratis.test"}, MessageHeaders{})
	if s.Level != ShieldCaution {
		t.Fatalf("direccion ajena en el nombre: %+v", s)
	}
	s = assess(Address{Name: "x@gratis.test", Email: "X@gratis.test"}, MessageHeaders{})
	if slices.Contains(reasons(s), ReasonEmbeddedAddress) {
		t.Fatal("la misma direccion no es un disfraz")
	}
	in := ShieldInput{
		From:       []Address{{Email: "ventas@proveedor.test"}},
		ReplyTo:    []Address{{Email: "ventas@proveedor.test"}, {Email: "cuentas@otro.test"}},
		OwnDomains: []string{"empresa.pe"},
	}
	if s := AssessSender(in); s.Level != ShieldCaution || !slices.Contains(reasons(s), ReasonReplyToMismatch) {
		t.Fatalf("Reply-To a otro dominio: %+v", s)
	}
	in.ReplyTo = []Address{{Email: "ana@empresa.pe"}}
	if s := AssessSender(in); slices.Contains(reasons(s), ReasonReplyToMismatch) {
		t.Fatal("un Reply-To propio no es sospechoso")
	}
}

func TestEscudoSinRemitenteYParcial(t *testing.T) {
	s := AssessSender(ShieldInput{Partial: true})
	if s.Level != ShieldNone || !s.Partial || len(s.Reasons) != 0 {
		t.Fatalf("%+v", s)
	}
	if DomainOf("sin-arroba") != "" || DomainOf("x@") != "" || DomainOf("A@B.PE.") != "b.pe" {
		t.Fatal("DomainOf")
	}
	if NormalizePersonName("  José   PÉREZ-Ruiz ") != "jose perez ruiz" {
		t.Fatalf("%q", NormalizePersonName("  José   PÉREZ-Ruiz "))
	}
}

func TestGroupThreadsUneConversacionesPorReferencias(t *testing.T) {
	msgs := []ThreadMessage{
		{UID: 9, MessageID: "<d@x>", References: []string{"a@x", "b@x"}},
		{UID: 8, MessageID: "z@y"},
		{UID: 7, MessageID: "c@x", InReplyTo: []string{"ausente@x"}},
		{UID: 6, MessageID: "b@x", InReplyTo: []string{"a@x"}},
		{UID: 5, MessageID: "e@x", References: []string{"ausente@x"}},
		{UID: 4},
		{UID: 3, MessageID: "A@X"},
		{UID: 2, MessageID: "no valido con espacios", InReplyTo: []string{"<>"}},
	}
	got := GroupThreads(msgs)
	want := [][]uint32{{9, 6, 3}, {8}, {7, 5}, {4}, {2}}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Fatalf("grupo %d: %v, quiero %v (todo %v)", i, got[i], want[i], got)
		}
	}
	if len(GroupThreads(nil)) != 0 {
		t.Fatal("vacio")
	}
}

func TestParticipantsSinRepetirYAcotados(t *testing.T) {
	var envs []Envelope
	for _, e := range []string{"a@x", "A@x", "b@x", "c@x", "d@x", "e@x", "f@x"} {
		envs = append(envs, Envelope{From: []Address{{Email: e}}})
	}
	got := Participants(envs)
	if len(got) != maxThreadParticipants || got[0].Email != "a@x" || got[1].Email != "b@x" {
		t.Fatalf("%v", got)
	}
}
