package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

const (
	testLinkKey = "clave-de-firma-de-pruebas-con-mas-de-32-caracteres"
	testCell    = "pe-01"
)

func newTestSigner(t *testing.T) *QuarantineLinkSigner {
	t.Helper()
	s, err := NewQuarantineLinkSigner(testLinkKey, "https://app.example.com/", testCell, 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFirmaDelEnlaceValidaAlteradaYCaducada(t *testing.T) {
	s := newTestSigner(t)
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	c := QuarantineLinkClaims{TenantID: uuid.New(), MessageID: uuid.New(), Action: LinkRelease, ExpiresAt: now.Add(time.Hour).Unix()}
	sig := s.Sign(c)
	if !s.Verify(testCell, c, sig, now) {
		t.Fatal("la firma recien hecha debe valer")
	}

	altered := map[string]QuarantineLinkClaims{}
	other := c
	other.TenantID = uuid.New()
	altered["otra empresa"] = other
	other = c
	other.MessageID = uuid.New()
	altered["otro mensaje"] = other
	other = c
	other.Action = LinkDiscard
	altered["otra accion"] = other
	other = c
	other.ExpiresAt++
	altered["caducidad alargada"] = other
	other = c
	other.Action = "delete"
	altered["accion desconocida"] = other
	for name, claims := range altered {
		if s.Verify(testCell, claims, sig, now) {
			t.Errorf("%s: la firma no debe valer", name)
		}
	}

	flipped := []byte(sig)
	if flipped[10] == 'a' {
		flipped[10] = 'b'
	} else {
		flipped[10] = 'a'
	}
	for name, bad := range map[string]string{"alterada": string(flipped), "truncada": sig[:63], "vacia": "", "mayusculas": strings.ToUpper(sig)} {
		if s.Verify(testCell, c, bad, now) {
			t.Errorf("firma %s aceptada", name)
		}
	}

	if s.Verify(testCell, c, sig, time.Unix(c.ExpiresAt, 0)) {
		t.Error("en el segundo de caducidad el enlace ya no vale")
	}
	if s.Verify(testCell, c, sig, now.Add(2*time.Hour)) {
		t.Error("un enlace caducado no vale")
	}
	otherKey, err := NewQuarantineLinkSigner(testLinkKey+"-otra", "https://app.example.com", testCell, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if otherKey.Verify(testCell, c, sig, now) {
		t.Error("una firma de otra clave no vale")
	}
}

// legacySignature es la firma de los enlaces emitidos antes de llevar la celda, escrita a
// mano: fija el formato de los enlaces ya enviados.
func legacySignature(key string, c QuarantineLinkClaims) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte("quarantine-link\n" + c.TenantID.String() + "\n" + c.MessageID.String() + "\n" + string(c.Action) + "\n" + strconv.FormatInt(c.ExpiresAt, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// La celda va en la firma: un enlace solo vale en la celda que lo firmo, aunque la otra
// comparta la clave, y el segmento de la ruta tiene que ser esa celda.
func TestFirmaDelEnlaceAtadaALaCelda(t *testing.T) {
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	pe01 := newTestSigner(t)
	pe02, err := NewQuarantineLinkSigner(testLinkKey, "https://app.example.com", "pe-02", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if pe01.Cell() != testCell || pe02.Cell() != "pe-02" {
		t.Fatalf("celdas: %q %q", pe01.Cell(), pe02.Cell())
	}
	c := QuarantineLinkClaims{TenantID: uuid.New(), MessageID: uuid.New(), Action: LinkRelease, ExpiresAt: now.Add(time.Hour).Unix()}
	sig02 := pe02.Sign(c)
	if !pe02.Verify("pe-02", c, sig02, now) {
		t.Fatal("en su celda vale")
	}
	if pe01.Sign(c) == sig02 {
		t.Fatal("la misma clave en dos celdas no da la misma firma")
	}
	for name, ok := range map[string]bool{
		"segmento cambiado a la celda que lo recibe": pe01.Verify(testCell, c, sig02, now),
		"segmento de su celda en otra celda":         pe01.Verify("pe-02", c, sig02, now),
		"celda desconocida":                          pe02.Verify("zz-99", c, sig02, now),
		"sin celda":                                  pe02.Verify("", c, sig02, now),
		"celda en mayusculas":                        pe02.Verify("PE-02", c, sig02, now),
	} {
		if ok {
			t.Errorf("%s: no debe valer", name)
		}
	}
}

// Los enlaces sin celda ya enviados valen hasta que caducan, con su firma de siempre; la
// firma de una forma no vale por la otra.
func TestEnlaceSinCeldaDeAntes(t *testing.T) {
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	s := newTestSigner(t)
	c := QuarantineLinkClaims{TenantID: uuid.New(), MessageID: uuid.New(), Action: LinkDiscard, ExpiresAt: now.Add(time.Hour).Unix()}
	legacy := legacySignature(testLinkKey, c)
	if !s.VerifyLegacy(c, legacy, now) {
		t.Fatal("un enlace de antes vigente vale")
	}
	if s.VerifyLegacy(c, legacy, time.Unix(c.ExpiresAt, 0)) {
		t.Error("caducado ya no vale")
	}
	other := c
	other.Action = LinkRelease
	if s.VerifyLegacy(other, legacy, now) {
		t.Error("otra accion no vale")
	}
	if s.Verify(testCell, c, legacy, now) {
		t.Error("una firma sin celda no vale en la ruta con celda")
	}
	if s.VerifyLegacy(c, s.Sign(c), now) {
		t.Error("una firma con celda no vale en la ruta sin celda")
	}
	if s.VerifyLegacy(c, legacySignature(testLinkKey+"-otra", c), now) {
		t.Error("una firma de otra clave no vale")
	}
}

func TestURLDelEnlace(t *testing.T) {
	s := newTestSigner(t)
	now := time.Now()
	c := QuarantineLinkClaims{TenantID: uuid.New(), MessageID: uuid.New(), Action: LinkDiscard, ExpiresAt: now.Add(time.Hour).Unix()}
	qhash := strings.Repeat("ab", 32)
	raw := s.URL(c, qhash)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "app.example.com" || u.Path != "/api/v1/public/mail-security/quarantine/pe-01/discard" {
		t.Fatalf("enlace: %s", raw)
	}
	q := u.Query()
	if q.Get("t") != c.TenantID.String() || q.Get("q") != qhash || q.Get("e") == "" || !IsHexToken(q.Get("sig")) || q.Has("m") {
		t.Fatalf("parametros: %v", q)
	}
	if !s.Verify(testCell, c, q.Get("sig"), now) {
		t.Fatal("la firma del enlace debe verificar")
	}
}

func TestFirmanteExigeClaveBaseYVigencia(t *testing.T) {
	for name, tc := range map[string]struct {
		key, base, cell string
		ttl             time.Duration
	}{
		"clave corta":         {"corta", "https://app.example.com", testCell, time.Hour},
		"sin base":            {testLinkKey, "", testCell, time.Hour},
		"base relativa":       {testLinkKey, "/api", testCell, time.Hour},
		"otro esquema":        {testLinkKey, "ftp://app.example.com", testCell, time.Hour},
		"vigencia nula":       {testLinkKey, "https://app.example.com", testCell, 0},
		"vigencia menor":      {testLinkKey, "https://app.example.com", testCell, -time.Hour},
		"sin celda":           {testLinkKey, "https://app.example.com", "", time.Hour},
		"celda en mayusculas": {testLinkKey, "https://app.example.com", "PE-01", time.Hour},
		"celda con barra":     {testLinkKey, "https://app.example.com", "pe/01", time.Hour},
		"celda con punto":     {testLinkKey, "https://app.example.com", "..", time.Hour},
		"celda larguisima":    {testLinkKey, "https://app.example.com", strings.Repeat("a", 64), time.Hour},
	} {
		if _, err := NewQuarantineLinkSigner(tc.key, tc.base, tc.cell, tc.ttl); err == nil {
			t.Errorf("%s: se esperaba error", name)
		}
	}
}

func TestIsHexToken(t *testing.T) {
	for s, want := range map[string]bool{
		strings.Repeat("0f", 32): true,
		strings.Repeat("0F", 32): false,
		strings.Repeat("0g", 32): false,
		strings.Repeat("0", 63):  false,
		"":                       false,
	} {
		if IsHexToken(s) != want {
			t.Errorf("%q: se esperaba %v", s, want)
		}
	}
}

const hostileTemplate = `<p>{{.Count}} mensajes para {{.Mailbox}} (enlaces hasta {{.LinksExpireAt}})</p>
<ul>{{range .Messages}}<li title="{{.Subject}}">{{.Subject}} de {{.Sender}} ({{.Score}}, {{.Date}})
<a href="{{.ReleaseURL}}">Liberar</a> <a href="{{.DiscardURL}}">Descartar</a></li>{{end}}</ul>
<script>var ultimo = "{{range .Messages}}{{.Subject}}{{end}}";</script>`

func hostileGroup() NoticeGroup {
	created := time.Date(2026, 9, 13, 8, 30, 0, 0, time.UTC)
	return NoticeGroup{Mailbox: "ana@acme.com", Messages: []QuarantineItem{{
		ID:        uuid.New(),
		Subject:   `</script><script>alert(1)</script>"><img src=x onerror=alert(2)>`,
		Sender:    `"><svg onload=alert(3)>@evil.test`,
		Score:     decimal.RequireFromString("13.456"),
		CreatedAt: created,
		QHash:     strings.Repeat("cd", 32),
	}}}
}

// El asunto y el remitente de un mensaje en cuarentena son contenido hostil: salen
// escapados en texto, en atributo y dentro de un script de la plantilla.
func TestAvisoEscapaTextoHostil(t *testing.T) {
	tpl, err := ParseNoticeTemplate(hostileTemplate)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestSigner(t)
	exp := time.Date(2026, 9, 16, 8, 30, 0, 0, time.UTC)
	data := NewNoticeData(hostileGroup(), exp, func(m QuarantineItem, a QuarantineLinkAction) string {
		return s.URL(QuarantineLinkClaims{TenantID: uuid.New(), MessageID: m.ID, Action: a, ExpiresAt: exp.Unix()}, m.QHash)
	})
	out, err := tpl.Render(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"<script>alert(1)", "<img src=x", "<svg onload", `"><img`, `"><svg`} {
		if strings.Contains(out, forbidden) {
			t.Errorf("texto hostil sin escapar %q en:\n%s", forbidden, out)
		}
	}
	if n := strings.Count(out, "</script>"); n != 1 {
		t.Errorf("solo el cierre del script de la plantilla: %d en\n%s", n, out)
	}
	for _, want := range []string{
		"&lt;img src=x onerror=alert(2)&gt;",
		"&#34;&gt;&lt;svg onload=alert(3)&gt;@evil.test",
		"1 mensajes para ana@acme.com",
		"(13.46, 2026-09-13 08:30 UTC)",
		"enlaces hasta 2026-09-16 08:30 UTC",
		`href="https://app.example.com/api/v1/public/mail-security/quarantine/pe-01/release?e=`,
		`href="https://app.example.com/api/v1/public/mail-security/quarantine/pe-01/discard?e=`,
		"&amp;q=" + strings.Repeat("cd", 32),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("falta %q en:\n%s", want, out)
		}
	}
}

func TestTextoDeTercerosSaneado(t *testing.T) {
	if got := CleanThirdPartyText("  hola\r\nmundo\u202e\u2066x\xff  ", 200); got != "hola  mundox\uFFFD" {
		t.Errorf("saneado: %q", got)
	}
	long := strings.Repeat("a", 300)
	if got := CleanThirdPartyText(long, 200); got != strings.Repeat("a", 200)+"..." {
		t.Errorf("recorte: %d runas", len([]rune(got)))
	}
	if got := CleanThirdPartyText(strings.Repeat("ñ", 10), 10); got != strings.Repeat("ñ", 10) {
		t.Errorf("justo en el tope no se recorta: %q", got)
	}
}

func TestAvisoTopeDeTamano(t *testing.T) {
	tpl, err := ParseNoticeTemplate(`{{range .Messages}}{{range $.Messages}}{{range $.Messages}}{{.Subject}}{{end}}{{end}}{{end}}`)
	if err != nil {
		t.Fatal(err)
	}
	g := NoticeGroup{Mailbox: "ana@acme.com"}
	for i := 0; i < 100; i++ {
		g.Messages = append(g.Messages, QuarantineItem{ID: uuid.New(), Subject: strings.Repeat("x", 200)})
	}
	if _, err := tpl.Render(NewNoticeData(g, time.Now(), func(QuarantineItem, QuarantineLinkAction) string { return "" })); err != ErrNoticeTooLarge {
		t.Fatalf("el HTML se corta en el tope: %v", err)
	}
}

func TestValidateQuarantineNotify(t *testing.T) {
	ok := QuarantineNotify{Enabled: true, Sender: "cuarentena@acme.com", Subject: "Correo retenido", HTMLTemplate: hostileTemplate}
	if err := ValidateQuarantineNotify(ok); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*QuarantineNotify){
		"sin remitente":         func(n *QuarantineNotify) { n.Sender = "" },
		"remitente sin arroba":  func(n *QuarantineNotify) { n.Sender = "cuarentena" },
		"remitente con nombre":  func(n *QuarantineNotify) { n.Sender = "Acme <cuarentena@acme.com>" },
		"remitente sin dominio": func(n *QuarantineNotify) { n.Sender = "cuarentena@" },
		"sin asunto":            func(n *QuarantineNotify) { n.Subject = "  " },
		"asunto de dos lineas":  func(n *QuarantineNotify) { n.Subject = "Correo\r\nBcc: x@y.com" },
		"sin plantilla":         func(n *QuarantineNotify) { n.HTMLTemplate = "" },
		"plantilla rota":        func(n *QuarantineNotify) { n.HTMLTemplate = "{{range .Messages}}" },
		"campo inexistente":     func(n *QuarantineNotify) { n.HTMLTemplate = "{{range .Messages}}{{.Asunto}}{{end}}" },
	} {
		n := ok
		mutate(&n)
		if err := ValidateQuarantineNotify(n); err == nil || !strings.Contains(err.Error(), "notify.") {
			t.Errorf("%s: se esperaba un error de validacion, hubo %v", name, err)
		}
	}
}

func TestAgrupadoPorBuzonYClave(t *testing.T) {
	base := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	older := QuarantineItem{ID: uuid.New(), Rcpt: "ana@acme.com", CreatedAt: base.Add(-time.Hour)}
	newest := QuarantineItem{ID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), Rcpt: "ana@acme.com", CreatedAt: base}
	tie := QuarantineItem{ID: uuid.MustParse("ffffffff-0000-0000-0000-000000000000"), Rcpt: "ana@acme.com", CreatedAt: base}
	luis := QuarantineItem{ID: uuid.New(), Rcpt: "luis@acme.com", CreatedAt: base.Add(-2 * time.Hour)}

	groups := GroupByMailbox([]QuarantineItem{luis, older, newest, tie})
	if len(groups) != 2 || groups[0].Mailbox != "ana@acme.com" || groups[1].Mailbox != "luis@acme.com" {
		t.Fatalf("grupos: %+v", groups)
	}
	ana := groups[0]
	if len(ana.Messages) != 3 || ana.Latest().ID != tie.ID || ana.Messages[1].ID != newest.ID || ana.Messages[2].ID != older.ID {
		t.Fatalf("del mas reciente al mas antiguo, a igualdad de fecha por id descendente: %+v", ana.Messages)
	}
	if ids := ana.IDs(); len(ids) != 3 || ids[0] != tie.ID {
		t.Fatalf("ids: %v", ids)
	}
	if GroupByMailbox(nil) != nil {
		t.Fatal("sin pendientes no hay grupos")
	}

	if key := NoticeIdempotencyKey("ana@acme.com", tie.ID); key != "quarantine-notice:ana@acme.com:"+tie.ID.String() {
		t.Fatalf("clave: %s", key)
	}
	long := strings.Repeat("a", 180) + "@acme.com"
	key := NoticeIdempotencyKey(long, tie.ID)
	if len(key) > 200 || !strings.HasPrefix(key, "quarantine-notice:") || !strings.HasSuffix(key, ":"+tie.ID.String()) || strings.Contains(key, "@") {
		t.Fatalf("clave de un buzon largo: %s (%d)", key, len(key))
	}
	if NoticeIdempotencyKey(long, tie.ID) != key {
		t.Fatal("la clave es estable")
	}
}
