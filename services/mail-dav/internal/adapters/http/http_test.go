package http

import (
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const base = "/api/v1/dav"

var (
	ana  = apptest.Account{Password: "clave-de-ana", Principal: domain.Principal{TenantID: uuid.New(), MailboxID: uuid.New(), Username: "ana@acme.test"}}
	bea  = apptest.Account{Password: "clave-de-bea", Principal: domain.Principal{TenantID: uuid.New(), MailboxID: uuid.New(), Username: "bea@beta.test"}}
	cris = apptest.Account{Password: "clave-de-cris", Principal: domain.Principal{TenantID: ana.Principal.TenantID, MailboxID: uuid.New(), Username: "cris@acme.test"}}
)

var limits = domain.Limits{MaxVCardBytes: 4096, MaxVCardProperties: 30, MaxContactsPerMailbox: 4, MaxAddressbooksPerMailbox: 2, MaxChangesRetained: 50}

type harness struct {
	t     *testing.T
	h     *Handler
	auth  *apptest.Auth
	store *apptest.Store
	logs  *observer.ObservedLogs
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	core, logs := observer.New(zap.DebugLevel)
	logger := zap.New(core)
	auth, store := apptest.NewAuth(ana, bea, cris), apptest.NewStore()
	uc, err := app.New(app.Deps{Auth: auth, Tenant: apptest.Binder{}, Store: store, Logger: logger,
		Config: app.Config{Limits: limits, DefaultAddressbookName: "Contactos"}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(uc, Config{BasePath: base, Realm: "Contactos", MaxXMLBytes: 8 << 10, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, h: h, auth: auth, store: store, logs: logs}
}

type request struct {
	method, path, body string
	user               apptest.Account
	noAuth             bool
	headers            map[string]string
}

func (hn *harness) do(rq request) *httptest.ResponseRecorder {
	hn.t.Helper()
	req := httptest.NewRequest(rq.method, "http://mail.acme.test"+rq.path, strings.NewReader(rq.body))
	req.RemoteAddr = "10.0.0.5:4000"
	req.Header.Set("X-Real-IP", "203.0.113.7")
	if !rq.noAuth {
		acc := rq.user
		if acc.Principal.Username == "" {
			acc = ana
		}
		req.SetBasicAuth(acc.Principal.Username, acc.Password)
	}
	for k, v := range rq.headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	hn.h.ServeHTTP(rec, req)
	return rec
}

func (hn *harness) as(acc apptest.Account, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	hn.t.Helper()
	h := map[string]string{}
	for i := 0; i+1 < len(headers); i += 2 {
		h[headers[i]] = headers[i+1]
	}
	return hn.do(request{method: method, path: path, body: body, user: acc, headers: h})
}

func (hn *harness) req(method, path, body string, headers ...string) *httptest.ResponseRecorder {
	hn.t.Helper()
	return hn.as(ana, method, path, body, headers...)
}

func expectStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, quiero %d: %s", rec.Code, want, rec.Body.String())
	}
}

type multiResp struct {
	SyncToken string `xml:"sync-token"`
	Responses []struct {
		Href      string `xml:"href"`
		Status    string `xml:"status"`
		Propstats []struct {
			Status string `xml:"status"`
			Prop   struct {
				Items []struct {
					XMLName  xml.Name
					Text     string `xml:",chardata"`
					Children []struct {
						XMLName xml.Name
						Text    string `xml:",chardata"`
					} `xml:",any"`
				} `xml:",any"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

func parseMulti(t *testing.T, rec *httptest.ResponseRecorder) multiResp {
	t.Helper()
	expectStatus(t, rec, http.StatusMultiStatus)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Fatalf("content-type %q", ct)
	}
	var ms multiResp
	if err := xml.Unmarshal(rec.Body.Bytes(), &ms); err != nil {
		t.Fatalf("respuesta que no es XML: %v\n%s", err, rec.Body.String())
	}
	return ms
}

// prop devuelve el texto de una propiedad 200 de la respuesta i (o su primer href hijo), y si existe.
func (m multiResp) prop(i int, local string) (string, bool) {
	for _, ps := range m.Responses[i].Propstats {
		if !strings.Contains(ps.Status, "200") {
			continue
		}
		for _, it := range ps.Prop.Items {
			if it.XMLName.Local == local {
				if len(it.Children) > 0 && it.Text == "" {
					return strings.TrimSpace(it.Children[0].Text), true
				}
				return it.Text, true
			}
		}
	}
	return "", false
}

func (m multiResp) missing(i int, local string) bool {
	for _, ps := range m.Responses[i].Propstats {
		if !strings.Contains(ps.Status, "404") {
			continue
		}
		for _, it := range ps.Prop.Items {
			if it.XMLName.Local == local {
				return true
			}
		}
	}
	return false
}

func (m multiResp) hrefs() []string {
	out := make([]string, len(m.Responses))
	for i, r := range m.Responses {
		out[i] = r.Href
	}
	return out
}

func vcard(uid, name string) string {
	return fmt.Sprintf("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:%s\r\nFN:%s\r\nN:;%s;;;\r\nEMAIL;TYPE=WORK:%s@acme.test\r\nEND:VCARD\r\n", uid, name, name, uid)
}

const (
	homePath = base + "/addressbooks/ana@acme.test/"
	bookPath = homePath + "contacts/"
	textCard = "text/vcard; charset=utf-8"
)

func (hn *harness) putCard(res, uid, name string, headers ...string) *httptest.ResponseRecorder {
	hn.t.Helper()
	return hn.req("PUT", bookPath+res, vcard(uid, name), append([]string{"Content-Type", textCard}, headers...)...)
}

// Cuerpos de peticion como los envian los clientes reales, escritos a mano segun RFC 4918, 6352 y 6578.
const (
	// DAVx5: descubrimiento del principal, del home set y de las libretas.
	davx5CurrentUserPrincipal = `<?xml version="1.0" encoding="utf-8" ?>
<d:propfind xmlns:d="DAV:"><d:prop><d:current-user-principal/></d:prop></d:propfind>`
	davx5HomeSet = `<?xml version="1.0" encoding="utf-8" ?>
<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav"><d:prop><c:addressbook-home-set/></d:prop></d:propfind>`
	davx5Books = `<?xml version="1.0" encoding="utf-8" ?>
<d:propfind xmlns:d="DAV:" xmlns:cs="http://calendarserver.org/ns/" xmlns:c="urn:ietf:params:xml:ns:carddav"><d:prop>` +
		`<d:resourcetype/><d:displayname/><d:current-user-privilege-set/><cs:getctag/><c:addressbook-description/><c:supported-address-data/><d:sync-token/>` +
		`</d:prop></d:propfind>`
	davx5Sync = `<?xml version="1.0" encoding="utf-8" ?>
<d:sync-collection xmlns:d="DAV:"><d:sync-token>%s</d:sync-token><d:sync-level>1</d:sync-level><d:prop><d:getcontenttype/><d:getetag/></d:prop></d:sync-collection>`
	davx5Multiget = `<?xml version="1.0" encoding="utf-8" ?>
<c:addressbook-multiget xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav"><d:prop><d:getetag/><c:address-data/></d:prop>%s</c:addressbook-multiget>`

	// iOS: prefijos A y C, propiedades que este servidor no conoce (add-member) y consulta con filtro vacio.
	iosPrincipal = `<?xml version="1.0" encoding="UTF-8"?>
<A:propfind xmlns:A="DAV:"><A:prop><A:current-user-principal/><A:principal-URL/><A:resourcetype/></A:prop></A:propfind>`
	iosBook = `<?xml version="1.0" encoding="UTF-8"?>
<A:propfind xmlns:A="DAV:"><A:prop><A:add-member/><C:supported-address-data xmlns:C="urn:ietf:params:xml:ns:carddav"/>` +
		`<D:getctag xmlns:D="http://calendarserver.org/ns/"/><A:displayname/><A:resourcetype/><A:sync-token/><A:supported-report-set/></A:prop></A:propfind>`
	iosQueryAll = `<?xml version="1.0" encoding="UTF-8"?>
<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:prop><D:getetag/></D:prop><C:filter/></C:addressbook-query>`

	// Thunderbird: el nombre de la libreta y sus privilegios, y la consulta por texto.
	thunderbirdBook = `<?xml version="1.0"?>
<D:propfind xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/"><D:prop><D:resourcetype/><D:displayname/><D:current-user-privilege-set/><CS:getctag/></D:prop></D:propfind>`
	queryByText = `<?xml version="1.0" encoding="utf-8"?>
<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:prop><D:getetag/><C:address-data/></D:prop>` +
		`<C:filter test="anyof"><C:prop-filter name="FN"><C:text-match collation="i;unicode-casemap" match-type="contains">%s</C:text-match></C:prop-filter>` +
		`<C:prop-filter name="EMAIL"><C:text-match match-type="starts-with">%s</C:text-match></C:prop-filter></C:filter>%s</C:addressbook-query>`
)

func TestOptionsSinCredenciales(t *testing.T) {
	hn := newHarness(t)
	rec := hn.do(request{method: "OPTIONS", path: base + "/", noAuth: true})
	expectStatus(t, rec, http.StatusOK)
	if dav := rec.Header().Get("DAV"); !strings.Contains(dav, "addressbook") || !strings.Contains(dav, "1") {
		t.Fatalf("DAV: %q", dav)
	}
	for _, m := range []string{"PROPFIND", "REPORT", "PUT", "DELETE", "MKCOL", "GET"} {
		if !strings.Contains(rec.Header().Get("Allow"), m) {
			t.Errorf("Allow sin %s: %q", m, rec.Header().Get("Allow"))
		}
	}
	if hn.auth.Calls != 0 {
		t.Fatal("OPTIONS no consulta a mail-auth")
	}
}

func TestSinCredencialesValidasNadaSeResponde(t *testing.T) {
	hn := newHarness(t)
	for name, rq := range map[string]request{
		"sin cabecera":        {method: "PROPFIND", path: base + "/", noAuth: true},
		"contrasena mala":     {method: "PROPFIND", path: base + "/", user: apptest.Account{Password: "mala", Principal: ana.Principal}},
		"usuario inexistente": {method: "PROPFIND", path: base + "/", user: apptest.Account{Password: "x", Principal: domain.Principal{Username: "nadie@acme.test"}}},
		"cualquier ruta":      {method: "GET", path: bookPath + "no-existe.vcf", noAuth: true},
	} {
		rec := hn.do(rq)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d", name, rec.Code)
		}
		if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, `Basic realm="Contactos"`) {
			t.Errorf("%s: WWW-Authenticate %q", name, got)
		}
		if strings.Contains(rec.Body.String(), "clave") {
			t.Errorf("%s: el cuerpo repite datos de la peticion", name)
		}
	}
	// Ni una ruta que existe ni una que no distinguen nada sin credenciales.
	a := hn.do(request{method: "GET", path: bookPath + "no-existe.vcf", noAuth: true})
	b := hn.do(request{method: "GET", path: base + "/otra/cosa", noAuth: true})
	if a.Code != b.Code || a.Body.String() != b.Body.String() {
		t.Fatal("las respuestas sin credenciales no deben depender de la ruta")
	}
}

func TestMailAuthCaidoEsUn503YNoUnaContrasenaMala(t *testing.T) {
	hn := newHarness(t)
	hn.auth.Unavailable = true
	rec := hn.req("PROPFIND", base+"/", "")
	expectStatus(t, rec, http.StatusServiceUnavailable)
	if rec.Header().Get("WWW-Authenticate") != "" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("un cliente que recibe 401 descarta su contrasena: %v", rec.Header())
	}
}

func TestCadaPeticionSeAutenticaYLlevaLaIPRealAMailAuth(t *testing.T) {
	hn := newHarness(t)
	hn.req("PROPFIND", base+"/", "", "Depth", "0")
	hn.req("PROPFIND", base+"/", "", "Depth", "0")
	if hn.auth.Calls != 2 || hn.auth.LastIP != "203.0.113.7" {
		t.Fatalf("llamadas %d, IP %q", hn.auth.Calls, hn.auth.LastIP)
	}
}

func TestLaContrasenaNoApareceEnLosRegistros(t *testing.T) {
	hn := newHarness(t)
	hn.do(request{method: "PROPFIND", path: base + "/", user: apptest.Account{Password: "contrasena-secreta-9", Principal: ana.Principal}})
	hn.auth.Unavailable = true
	hn.req("PROPFIND", base+"/", "")
	hn.req("PUT", bookPath+"x.vcf", "no es un vCard", "Content-Type", textCard)
	basic := base64.StdEncoding.EncodeToString([]byte(ana.Principal.Username + ":" + ana.Password))
	for _, e := range hn.logs.All() {
		dump := fmt.Sprint(e.Message, e.ContextMap())
		for _, secret := range []string{"contrasena-secreta-9", ana.Password, basic} {
			if strings.Contains(dump, secret) {
				t.Fatalf("un registro contiene una credencial: %s", dump)
			}
		}
	}
}

func TestDescubrimientoComoDAVx5(t *testing.T) {
	hn := newHarness(t)

	ms := parseMulti(t, hn.req("PROPFIND", base+"/", davx5CurrentUserPrincipal, "Depth", "0"))
	principal, ok := ms.prop(0, "current-user-principal")
	if !ok || principal != base+"/principals/ana@acme.test/" {
		t.Fatalf("principal: %q %v", principal, ok)
	}

	ms = parseMulti(t, hn.req("PROPFIND", principal, davx5HomeSet, "Depth", "0"))
	home, ok := ms.prop(0, "addressbook-home-set")
	if !ok || home != homePath {
		t.Fatalf("home set: %q %v", home, ok)
	}

	ms = parseMulti(t, hn.req("PROPFIND", home, davx5Books, "Depth", "1"))
	if len(ms.Responses) != 2 || ms.Responses[0].Href != homePath || ms.Responses[1].Href != bookPath {
		t.Fatalf("home + libreta por defecto: %v", ms.hrefs())
	}
	if name, _ := ms.prop(1, "displayname"); name != "Contactos" {
		t.Fatalf("displayname: %q", name)
	}
	if ctag, ok := ms.prop(1, "getctag"); !ok || ctag == "" {
		t.Fatalf("getctag: %q", ctag)
	}
	if tok, ok := ms.prop(1, "sync-token"); !ok || !strings.HasPrefix(tok, "urn:mail-dav:sync:") {
		t.Fatalf("sync-token: %q", tok)
	}
	if _, ok := ms.prop(1, "supported-address-data"); !ok {
		t.Fatal("supported-address-data")
	}
	body := hn.req("PROPFIND", home, davx5Books, "Depth", "1").Body.String()
	for _, want := range []string{"addressbook", "collection", "privilege", "3.0", "4.0"} {
		if !strings.Contains(body, want) {
			t.Errorf("la respuesta no menciona %q", want)
		}
	}
}

func TestDescubrimientoComoIOSConPropiedadesDesconocidas(t *testing.T) {
	hn := newHarness(t)
	ms := parseMulti(t, hn.req("PROPFIND", base+"/", iosPrincipal, "Depth", "0"))
	if _, ok := ms.prop(0, "current-user-principal"); !ok {
		t.Fatal("current-user-principal")
	}
	if !ms.missing(0, "principal-URL") {
		t.Fatal("la raiz no es un principal: principal-URL va en 404")
	}
	hn.req("PROPFIND", homePath, "", "Depth", "1") // crea la libreta por defecto
	ms = parseMulti(t, hn.req("PROPFIND", bookPath, iosBook, "Depth", "0"))
	if !ms.missing(0, "add-member") {
		t.Fatal("add-member no esta soportada y debe ir en un propstat 404")
	}
	if _, ok := ms.prop(0, "supported-report-set"); !ok {
		t.Fatal("supported-report-set")
	}
	body := hn.req("PROPFIND", bookPath, iosBook, "Depth", "0").Body.String()
	for _, want := range []string{"sync-collection", "addressbook-query", "addressbook-multiget"} {
		if !strings.Contains(body, want) {
			t.Errorf("supported-report-set sin %s", want)
		}
	}
}

func TestPropfindComoThunderbirdYSinCuerpo(t *testing.T) {
	hn := newHarness(t)
	hn.req("PROPFIND", homePath, "", "Depth", "1")
	ms := parseMulti(t, hn.req("PROPFIND", bookPath, thunderbirdBook, "Depth", "0"))
	if name, _ := ms.prop(0, "displayname"); name != "Contactos" {
		t.Fatalf("displayname: %q", name)
	}
	if _, ok := ms.prop(0, "current-user-privilege-set"); !ok {
		t.Fatal("privilegios")
	}
	// Sin cuerpo es allprop; sin Depth se toma 1.
	all := parseMulti(t, hn.req("PROPFIND", homePath, ""))
	if len(all.Responses) != 2 {
		t.Fatalf("sin Depth: %v", all.hrefs())
	}
	if _, ok := all.prop(1, "getctag"); !ok {
		t.Fatal("allprop incluye getctag")
	}
	names := parseMulti(t, hn.req("PROPFIND", bookPath, `<propfind xmlns="DAV:"><propname/></propfind>`, "Depth", "0"))
	if _, ok := names.prop(0, "getetag"); ok {
		t.Fatal("una libreta no tiene getetag")
	}
	if v, ok := names.prop(0, "displayname"); !ok || v != "" {
		t.Fatalf("propname devuelve solo los nombres: %q %v", v, ok)
	}
}

func TestProfundidadInfinitaSeRechaza(t *testing.T) {
	hn := newHarness(t)
	rec := hn.req("PROPFIND", base+"/", "", "Depth", "infinity")
	expectStatus(t, rec, http.StatusForbidden)
	if !strings.Contains(rec.Body.String(), "propfind-finite-depth") {
		t.Fatalf("cuerpo: %s", rec.Body.String())
	}
}

func TestCicloDeUnContactoConEtags(t *testing.T) {
	hn := newHarness(t)
	hn.req("PROPFIND", homePath, "", "Depth", "1")
	rec := hn.putCard("uno.vcf", "uno", "Uno", "If-None-Match", "*")
	expectStatus(t, rec, http.StatusCreated)
	etag := rec.Header().Get("ETag")
	if want := `"` + domain.ETagOf(vcard("uno", "Uno")) + `"`; etag != want {
		t.Fatalf("ETag %q, quiero %q", etag, want)
	}

	get := hn.req("GET", bookPath+"uno.vcf", "")
	expectStatus(t, get, http.StatusOK)
	if get.Body.String() != vcard("uno", "Uno") || get.Header().Get("ETag") != etag || !strings.HasPrefix(get.Header().Get("Content-Type"), "text/vcard") {
		t.Fatalf("GET: %v %q", get.Header(), get.Body.String())
	}
	head := hn.req("HEAD", bookPath+"uno.vcf", "")
	expectStatus(t, head, http.StatusOK)
	if head.Body.Len() != 0 || head.Header().Get("Content-Length") == "" {
		t.Fatalf("HEAD: %d bytes", head.Body.Len())
	}
	expectStatus(t, hn.req("GET", bookPath+"uno.vcf", "", "If-None-Match", etag), http.StatusNotModified)

	expectStatus(t, hn.putCard("uno.vcf", "uno", "Uno otra vez", "If-None-Match", "*"), http.StatusPreconditionFailed)
	expectStatus(t, hn.putCard("uno.vcf", "uno", "Con etag viejo", "If-Match", `"deadbeef"`), http.StatusPreconditionFailed)
	rec = hn.putCard("uno.vcf", "uno", "Editado", "If-Match", etag)
	expectStatus(t, rec, http.StatusNoContent)
	if rec.Header().Get("ETag") == etag {
		t.Fatal("el ETag cambia con el contenido")
	}

	expectStatus(t, hn.req("DELETE", bookPath+"uno.vcf", "", "If-Match", etag), http.StatusPreconditionFailed)
	expectStatus(t, hn.req("DELETE", bookPath+"uno.vcf", "", "If-Match", rec.Header().Get("ETag")), http.StatusNoContent)
	expectStatus(t, hn.req("GET", bookPath+"uno.vcf", ""), http.StatusNotFound)
	expectStatus(t, hn.req("DELETE", bookPath+"uno.vcf", ""), http.StatusNotFound)
	// Un etag debil nunca cumple If-Match.
	hn.putCard("dos.vcf", "dos", "Dos")
	expectStatus(t, hn.req("DELETE", bookPath+"dos.vcf", "", "If-Match", `W/"`+domain.ETagOf(vcard("dos", "Dos"))+`"`), http.StatusPreconditionFailed)
}

func TestPutValidaContenidoTipoYTamano(t *testing.T) {
	hn := newHarness(t)
	hn.req("PROPFIND", homePath, "", "Depth", "1")
	put := func(body string, headers ...string) *httptest.ResponseRecorder {
		return hn.req("PUT", bookPath+"x.vcf", body, headers...)
	}
	expectStatus(t, put(vcard("a", "A"), "Content-Type", "text/calendar"), http.StatusUnsupportedMediaType)
	expectStatus(t, put(vcard("a", "A"), "Content-Type", "text/vcard; charset=iso-8859-1"), http.StatusUnsupportedMediaType)
	for i, ct := range []string{"text/vcard", "text/x-vcard; charset=UTF-8", ""} {
		rec := hn.req("PUT", fmt.Sprintf("%st%d.vcf", bookPath, i), vcard(fmt.Sprintf("t%d", i), "T"), "Content-Type", ct)
		expectStatus(t, rec, http.StatusCreated)
	}
	rec := put("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:sin uid\r\nEND:VCARD\r\n", "Content-Type", textCard)
	expectStatus(t, rec, http.StatusForbidden)
	if !strings.Contains(rec.Body.String(), "valid-address-data") {
		t.Fatalf("cuerpo: %s", rec.Body.String())
	}
	expectStatus(t, put("<html>no soy un vCard</html>", "Content-Type", textCard), http.StatusForbidden)
	expectStatus(t, put(vcard("big", strings.Repeat("A", limits.MaxVCardBytes)), "Content-Type", textCard), http.StatusRequestEntityTooLarge)
	// Un cuerpo enorme se corta al llegar al tope, sin leerlo entero.
	huge := &countingReader{remaining: 50 << 20}
	req := httptest.NewRequest("PUT", "http://mail.acme.test"+bookPath+"huge.vcf", huge)
	req.SetBasicAuth(ana.Principal.Username, ana.Password)
	req.Header.Set("Content-Type", textCard)
	rec = httptest.NewRecorder()
	hn.h.ServeHTTP(rec, req)
	expectStatus(t, rec, http.StatusRequestEntityTooLarge)
	if huge.read > int64(limits.MaxVCardBytes)+(64<<10) {
		t.Fatalf("se leyeron %d bytes de un cuerpo que debia cortarse en %d", huge.read, limits.MaxVCardBytes)
	}
	if _, err := hn.store.GetContact(nil, ana.Principal, "contacts", "huge.vcf"); err == nil {
		t.Fatal("un contacto rechazado no se guarda")
	}
}

type countingReader struct {
	remaining int64
	read      int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > c.remaining {
		n = c.remaining
	}
	for i := int64(0); i < n; i++ {
		p[i] = 'A'
	}
	c.remaining -= n
	c.read += n
	return int(n), nil
}

func TestPutConflictosYLimites(t *testing.T) {
	hn := newHarness(t)
	expectStatus(t, hn.req("PUT", homePath+"noexiste/x.vcf", vcard("a", "A"), "Content-Type", textCard), http.StatusConflict)
	hn.req("PROPFIND", homePath, "", "Depth", "1")
	hn.putCard("a.vcf", "mismo-uid", "A")
	rec := hn.putCard("b.vcf", "mismo-uid", "B")
	expectStatus(t, rec, http.StatusConflict)
	if !strings.Contains(rec.Body.String(), "no-uid-conflict") || !strings.Contains(rec.Body.String(), bookPath+"a.vcf") {
		t.Fatalf("no-uid-conflict debe nombrar el recurso: %s", rec.Body.String())
	}
	for i := 0; i < limits.MaxContactsPerMailbox-1; i++ {
		expectStatus(t, hn.putCard(fmt.Sprintf("c%d.vcf", i), fmt.Sprintf("c%d", i), "C"), http.StatusCreated)
	}
	rec = hn.putCard("extra.vcf", "extra", "Extra")
	expectStatus(t, rec, http.StatusInsufficientStorage)
	if !strings.Contains(rec.Body.String(), "quota-not-exceeded") {
		t.Fatalf("cuerpo: %s", rec.Body.String())
	}
	expectStatus(t, hn.putCard("a.vcf", "mismo-uid", "A editada"), http.StatusNoContent)
}

func TestPutYGetNoAdmitenRutasQueNoSonContactos(t *testing.T) {
	hn := newHarness(t)
	hn.req("PROPFIND", homePath, "", "Depth", "1")
	for _, path := range []string{bookPath, homePath, base + "/", base + "/principals/ana@acme.test/"} {
		expectStatus(t, hn.req("PUT", path, vcard("a", "A"), "Content-Type", textCard), http.StatusMethodNotAllowed)
		expectStatus(t, hn.req("GET", path, ""), http.StatusMethodNotAllowed)
	}
	for _, path := range []string{homePath, base + "/", base + "/principals/ana@acme.test/"} {
		expectStatus(t, hn.req("DELETE", path, ""), http.StatusForbidden)
	}
	expectStatus(t, hn.req("PUT", bookPath+"a.txt", vcard("a", "A"), "Content-Type", textCard), http.StatusForbidden)
}

func TestMulticontactoYSincronizacionComoDAVx5(t *testing.T) {
	hn := newHarness(t)
	ms := parseMulti(t, hn.req("PROPFIND", homePath, davx5Books, "Depth", "1"))
	initial, _ := ms.prop(1, "sync-token")

	sync := func(token string) multiResp {
		return parseMulti(t, hn.req("REPORT", bookPath, fmt.Sprintf(davx5Sync, token), "Depth", "0"))
	}
	first := sync("")
	if len(first.Responses) != 0 || first.SyncToken != initial {
		t.Fatalf("libreta vacia: %+v (token %q, esperaba %q)", first.hrefs(), first.SyncToken, initial)
	}
	hn.putCard("a.vcf", "a", "Ana")
	hn.putCard("b.vcf", "b", "Beto")
	next := sync(first.SyncToken)
	if got := next.hrefs(); len(got) != 2 || got[0] != bookPath+"a.vcf" || got[1] != bookPath+"b.vcf" {
		t.Fatalf("cambios: %v", got)
	}
	if etag, ok := next.prop(0, "getetag"); !ok || etag != `"`+domain.ETagOf(vcard("a", "Ana"))+`"` {
		t.Fatalf("getetag: %q", etag)
	}
	if ct, _ := next.prop(0, "getcontenttype"); !strings.HasPrefix(ct, "text/vcard") {
		t.Fatalf("getcontenttype: %q", ct)
	}
	if next.SyncToken == first.SyncToken {
		t.Fatal("el token avanza con los cambios")
	}
	if idle := sync(next.SyncToken); len(idle.Responses) != 0 || idle.SyncToken != next.SyncToken {
		t.Fatalf("sin cambios: %v", idle.hrefs())
	}

	hn.putCard("a.vcf", "a", "Ana editada")
	hn.req("DELETE", bookPath+"b.vcf", "")
	delta := sync(next.SyncToken)
	if len(delta.Responses) != 2 || delta.Responses[0].Href != bookPath+"a.vcf" || delta.Responses[1].Href != bookPath+"b.vcf" ||
		!strings.Contains(delta.Responses[1].Status, "404") {
		t.Fatalf("edicion y borrado: %+v", delta.hrefs())
	}

	get := fmt.Sprintf(davx5Multiget, "<d:href>"+bookPath+"a.vcf</d:href><d:href>"+bookPath+"b.vcf</d:href><d:href>"+bookPath+"../a.vcf</d:href>")
	mg := parseMulti(t, hn.req("REPORT", bookPath, get, "Depth", "1"))
	if len(mg.Responses) != 3 {
		t.Fatalf("multiget: %v", mg.hrefs())
	}
	if data, ok := mg.prop(0, "address-data"); !ok || data != vcard("a", "Ana editada") {
		t.Fatalf("address-data: %q %v", data, ok)
	}
	if !strings.Contains(mg.Responses[1].Status, "404") || !strings.Contains(mg.Responses[2].Status, "404") {
		t.Fatalf("un borrado y un href con .. van en 404: %+v", mg.Responses)
	}
}

func TestSincronizacionInvalidaSePideDeNuevo(t *testing.T) {
	hn := newHarness(t)
	hn.req("PROPFIND", homePath, "", "Depth", "1")
	for _, token := range []string{"basura", "urn:mail-dav:sync:" + uuid.NewString() + ":3"} {
		rec := hn.req("REPORT", bookPath, fmt.Sprintf(davx5Sync, token))
		expectStatus(t, rec, http.StatusForbidden)
		if !strings.Contains(rec.Body.String(), "valid-sync-token") {
			t.Fatalf("cuerpo: %s", rec.Body.String())
		}
	}
	expectStatus(t, hn.req("REPORT", bookPath, strings.Replace(davx5Sync, "<d:sync-level>1", "<d:sync-level>infinite", 1)), http.StatusBadRequest)
}

func TestConsultasComoIOSYThunderbird(t *testing.T) {
	hn := newHarness(t)
	hn.req("PROPFIND", homePath, "", "Depth", "1")
	hn.putCard("a.vcf", "a", "Ana Perez")
	hn.putCard("b.vcf", "b", "Beto Ruiz")
	hn.putCard("c.vcf", "c", "Carla Perez")

	all := parseMulti(t, hn.req("REPORT", bookPath, iosQueryAll))
	if len(all.Responses) != 3 {
		t.Fatalf("filtro vacio devuelve todo: %v", all.hrefs())
	}
	if _, ok := all.prop(0, "address-data"); ok {
		t.Fatal("iOS solo pidio getetag")
	}

	byText := parseMulti(t, hn.req("REPORT", bookPath, fmt.Sprintf(queryByText, "perez", "b@", "")))
	if got := byText.hrefs(); len(got) != 3 {
		t.Fatalf("anyof: %v", got)
	}
	limited := parseMulti(t, hn.req("REPORT", bookPath, fmt.Sprintf(queryByText, "perez", "zzz", "<C:limit><C:nresults>1</C:nresults></C:limit>")))
	if len(limited.Responses) != 1 {
		t.Fatalf("limit: %v", limited.hrefs())
	}
	if data, ok := limited.prop(0, "address-data"); !ok || !strings.Contains(data, "Perez") {
		t.Fatalf("address-data: %q", data)
	}
	none := parseMulti(t, hn.req("REPORT", bookPath, fmt.Sprintf(queryByText, "nadie", "nadie", "")))
	if len(none.Responses) != 0 {
		t.Fatalf("sin coincidencias: %v", none.hrefs())
	}
}

func TestConsultasConFiltrosQueNoSeSaben(t *testing.T) {
	hn := newHarness(t)
	hn.req("PROPFIND", homePath, "", "Depth", "1")
	head := `<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:prop><D:getetag/></D:prop><C:filter>`
	for name, tc := range map[string]struct{ filter, want string }{
		"param-filter": {`<C:prop-filter name="TEL"><C:param-filter name="TYPE"/></C:prop-filter>`, "supported-filter"},
		"collation":    {`<C:prop-filter name="FN"><C:text-match collation="i;klingon">x</C:text-match></C:prop-filter>`, "supported-collation"},
		"match-type":   {`<C:prop-filter name="FN"><C:text-match match-type="regex">x</C:text-match></C:prop-filter>`, "supported-filter"},
	} {
		rec := hn.req("REPORT", bookPath, head+tc.filter+`</C:filter></C:addressbook-query>`)
		expectStatus(t, rec, http.StatusForbidden)
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: %s", name, rec.Body.String())
		}
	}
	expectStatus(t, hn.req("REPORT", bookPath, `<D:principal-match xmlns:D="DAV:"/>`), http.StatusForbidden)
	expectStatus(t, hn.req("REPORT", homePath, iosQueryAll), http.StatusForbidden)
	expectStatus(t, hn.req("REPORT", bookPath, ""), http.StatusBadRequest)
}

func TestMkcolYBorradoDeLibretas(t *testing.T) {
	hn := newHarness(t)
	extended := `<?xml version="1.0" encoding="utf-8" ?>
<D:mkcol xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:set><D:prop>
<D:resourcetype><D:collection/><C:addressbook/></D:resourcetype><D:displayname>Familia</D:displayname><C:addressbook-description>Los de casa</C:addressbook-description>
</D:prop></D:set></D:mkcol>`
	expectStatus(t, hn.req("MKCOL", homePath+"familia/", extended), http.StatusCreated)
	ms := parseMulti(t, hn.req("PROPFIND", homePath+"familia/", `<propfind xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><prop><displayname/><C:addressbook-description/></prop></propfind>`, "Depth", "0"))
	if name, _ := ms.prop(0, "displayname"); name != "Familia" {
		t.Fatalf("nombre: %q", name)
	}
	if d, _ := ms.prop(0, "addressbook-description"); d != "Los de casa" {
		t.Fatalf("descripcion: %q", d)
	}
	expectStatus(t, hn.req("MKCOL", homePath+"familia/", extended), http.StatusMethodNotAllowed)
	expectStatus(t, hn.req("MKCOL", homePath+"simple", ""), http.StatusCreated)
	expectStatus(t, hn.req("MKCOL", homePath+"tercera", ""), http.StatusInsufficientStorage)
	calendar := `<D:mkcol xmlns:D="DAV:"><D:set><D:prop><D:resourcetype><D:collection/><X:calendar xmlns:X="urn:ietf:params:xml:ns:caldav"/></D:resourcetype></D:prop></D:set></D:mkcol>`
	expectStatus(t, hn.req("MKCOL", homePath+"agenda", calendar), http.StatusForbidden)
	expectStatus(t, hn.req("MKCOL", homePath+"Mayusculas", ""), http.StatusForbidden)
	expectStatus(t, hn.req("MKCOL", homePath, ""), http.StatusForbidden)

	hn.req("PUT", homePath+"familia/x.vcf", vcard("x", "X"), "Content-Type", textCard)
	expectStatus(t, hn.req("DELETE", homePath+"familia/", ""), http.StatusNoContent)
	expectStatus(t, hn.req("GET", homePath+"familia/x.vcf", ""), http.StatusNotFound)
	expectStatus(t, hn.req("DELETE", homePath+"familia/", ""), http.StatusNotFound)
}

func TestUnBuzonNoVeLosDatosDeOtro(t *testing.T) {
	hn := newHarness(t)
	hn.req("PROPFIND", homePath, "", "Depth", "1")
	hn.putCard("secreto.vcf", "s", "Secreto de Ana")
	beaHome := base + "/addressbooks/bea@beta.test/"
	crisHome := base + "/addressbooks/cris@acme.test/"
	hn.as(bea, "PROPFIND", beaHome, "", "Depth", "1")
	hn.as(cris, "PROPFIND", crisHome, "", "Depth", "1")

	// Bea y Cris (misma empresa que Ana) piden el contacto de Ana por su URL.
	for name, acc := range map[string]apptest.Account{"otra empresa": bea, "misma empresa": cris} {
		for _, method := range []string{"GET", "HEAD", "DELETE", "PROPFIND"} {
			rec := hn.as(acc, method, bookPath+"secreto.vcf", "", "Depth", "0")
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s: %d", name, method, rec.Code)
			}
			if strings.Contains(rec.Body.String(), "Secreto") {
				t.Errorf("%s %s: filtra el contacto", name, method)
			}
		}
		expectStatus(t, hn.as(acc, "PROPFIND", homePath, "", "Depth", "1"), http.StatusNotFound)
		expectStatus(t, hn.as(acc, "PUT", bookPath+"nuevo.vcf", vcard("n", "N"), "Content-Type", textCard), http.StatusNotFound)
		expectStatus(t, hn.as(acc, "REPORT", bookPath, iosQueryAll), http.StatusNotFound)
		expectStatus(t, hn.as(acc, "MKCOL", homePath+"mia/", ""), http.StatusNotFound)
	}
	// Sus propias libretas se llaman igual y siguen vacias; el href de otro buzon es un 404 dentro de su multiget.
	for _, acc := range []struct {
		acc  apptest.Account
		home string
	}{{bea, beaHome}, {cris, crisHome}} {
		mg := parseMulti(t, hn.as(acc.acc, "REPORT", acc.home+"contacts/", fmt.Sprintf(davx5Multiget, "<d:href>"+bookPath+"secreto.vcf</d:href>"), "Depth", "1"))
		if len(mg.Responses) != 1 || !strings.Contains(mg.Responses[0].Status, "404") {
			t.Errorf("multiget con un href ajeno: %+v", mg.Responses)
		}
		all := parseMulti(t, hn.as(acc.acc, "REPORT", acc.home+"contacts/", iosQueryAll))
		if len(all.Responses) != 0 {
			t.Errorf("la libreta de otro buzon no trae contactos de Ana: %v", all.hrefs())
		}
	}
	expectStatus(t, hn.req("GET", bookPath+"secreto.vcf", ""), http.StatusOK)
}

func TestRutasConTruco(t *testing.T) {
	hn := newHarness(t)
	hn.req("PROPFIND", homePath, "", "Depth", "1")
	hn.putCard("a.vcf", "a", "A")
	for name, path := range map[string]string{
		"punto punto":              homePath + "contacts/../contacts/a.vcf",
		"punto punto codif":        homePath + "contacts/%2e%2e/contacts/a.vcf",
		"barra codificada":         homePath + "contacts%2Fa.vcf",
		"barra invertida":          homePath + "contacts/..%5Ca.vcf",
		"doble barra":              homePath + "contacts//a.vcf",
		"NUL":                      homePath + "contacts/a.vcf%00",
		"salto de linea":           homePath + "contacts/a%0a.vcf",
		"otro prefijo":             "/api/v1/otro/addressbooks/ana@acme.test/contacts/a.vcf",
		"prefijo pegado":           base + "x/addressbooks/ana@acme.test/contacts/a.vcf",
		"demasiado profundo":       bookPath + "a.vcf/mas/y/mas",
		"usuario de otro":          base + "/addressbooks/bea@beta.test/contacts/a.vcf",
		"usuario con barra":        base + "/addressbooks/ana@acme.test%2F..%2Fbea@beta.test/contacts/a.vcf",
		"contacto con barra final": bookPath + "a.vcf/",
		"principal ajeno":          base + "/principals/bea@beta.test/",
	} {
		for _, method := range []string{"GET", "PROPFIND", "DELETE"} {
			rec := hn.req(method, path, "")
			if rec.Code < 400 || rec.Code == http.StatusMultiStatus {
				t.Errorf("%s %s: %d", name, method, rec.Code)
			}
			if strings.Contains(rec.Body.String(), "BEGIN:VCARD") {
				t.Errorf("%s %s: devolvio datos", name, method)
			}
		}
	}
	expectStatus(t, hn.req("GET", bookPath+"a.vcf", ""), http.StatusOK)
	// El nombre del usuario en la URL se compara sin distinguir mayusculas, no como otro usuario.
	expectStatus(t, hn.req("GET", base+"/addressbooks/Ana@ACME.test/contacts/a.vcf", ""), http.StatusOK)
}

func TestXMLHostilNoSeInterpreta(t *testing.T) {
	hn := newHarness(t)
	hn.req("PROPFIND", homePath, "", "Depth", "1")
	xxe := `<?xml version="1.0"?><!DOCTYPE foo [<!ENTITY xxe SYSTEM "file:///etc/passwd">]>` +
		`<propfind xmlns="DAV:"><prop><displayname>&xxe;</displayname></prop></propfind>`
	rec := hn.req("PROPFIND", bookPath, xxe, "Depth", "0")
	expectStatus(t, rec, http.StatusBadRequest)
	if strings.Contains(rec.Body.String(), "root:") {
		t.Fatal("una entidad externa se resolvio")
	}
	lol := `<?xml version="1.0"?><!DOCTYPE lolz [<!ENTITY a "AAAAAAAAAA"><!ENTITY b "&a;&a;&a;&a;&a;&a;&a;&a;&a;&a;">]><propfind xmlns="DAV:"><prop><displayname>&b;</displayname></prop></propfind>`
	expectStatus(t, hn.req("PROPFIND", bookPath, lol, "Depth", "0"), http.StatusBadRequest)

	huge := `<propfind xmlns="DAV:"><prop>` + strings.Repeat("<x/>", 10000) + `</prop></propfind>`
	expectStatus(t, hn.req("PROPFIND", bookPath, huge, "Depth", "0"), http.StatusRequestEntityTooLarge)
	deep := `<propfind xmlns="DAV:"><prop>` + strings.Repeat("<a>", 800) + strings.Repeat("</a>", 800) + `</prop></propfind>`
	if rec := hn.req("PROPFIND", bookPath, deep, "Depth", "0"); rec.Code != http.StatusMultiStatus && rec.Code != http.StatusBadRequest {
		t.Fatalf("anidamiento profundo: %d", rec.Code)
	}
	expectStatus(t, hn.req("PROPFIND", bookPath, "<propfind", "Depth", "0"), http.StatusBadRequest)
	expectStatus(t, hn.req("REPORT", bookPath, `<?xml version="1.0"?><!DOCTYPE x [<!ENTITY e SYSTEM "http://127.0.0.1/">]><D:sync-collection xmlns:D="DAV:"><D:sync-token>&e;</D:sync-token></D:sync-collection>`), http.StatusBadRequest)
	// Lo que el cliente escribe en un nombre de libreta se escapa en las respuestas.
	expectStatus(t, hn.req("MKCOL", homePath+"xss", `<D:mkcol xmlns:D="DAV:"><D:set><D:prop><D:displayname>&lt;script&gt;alert(1)&lt;/script&gt;</D:displayname></D:prop></D:set></D:mkcol>`), http.StatusCreated)
	body := hn.req("PROPFIND", homePath+"xss/", "", "Depth", "0").Body.String()
	if strings.Contains(body, "<script>") || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("el nombre se escribio sin escapar: %s", body)
	}
}

func TestMetodosNoSoportados(t *testing.T) {
	hn := newHarness(t)
	for _, m := range []string{"PROPPATCH", "COPY", "MOVE", "LOCK", "UNLOCK", "POST", "PATCH", "TRACE"} {
		rec := hn.do(request{method: m, path: bookPath + "a.vcf", noAuth: true})
		if rec.Code != http.StatusMethodNotAllowed || !strings.Contains(rec.Header().Get("Allow"), "PROPFIND") {
			t.Errorf("%s: %d %q", m, rec.Code, rec.Header().Get("Allow"))
		}
	}
}

func TestLasRespuestasNoSeGuardanEnCache(t *testing.T) {
	hn := newHarness(t)
	rec := hn.req("PROPFIND", base+"/", "", "Depth", "0")
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("cabeceras: %v", rec.Header())
	}
}

func TestNewHandlerValidaSuConfiguracion(t *testing.T) {
	uc, _ := app.New(app.Deps{Config: app.Config{Limits: limits, DefaultAddressbookName: "x"}})
	for name, cfg := range map[string]Config{
		"sin prefijo":       {Realm: "r", MaxXMLBytes: 1},
		"prefijo con barra": {BasePath: "/api/v1/dav/", Realm: "r", MaxXMLBytes: 1},
		"prefijo raro":      {BasePath: "/API/../dav", Realm: "r", MaxXMLBytes: 1},
		"sin realm":         {BasePath: base, MaxXMLBytes: 1},
		"realm con comilla": {BasePath: base, Realm: `a"b`, MaxXMLBytes: 1},
		"sin tope de XML":   {BasePath: base, Realm: "r"},
	} {
		if _, err := NewHandler(uc, cfg); err == nil {
			t.Errorf("%s: debia rechazarse", name)
		}
	}
}
