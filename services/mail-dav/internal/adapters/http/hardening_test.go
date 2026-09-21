package http

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"go.uber.org/zap"
)

// spyStore anota con que opciones de lectura se pidio cada listado, y puede fallar el recorrido con el
// error que se le indique.
type spyStore struct {
	*apptest.Store
	contacts, changes, events, eventChanges []domain.ReadOptions
	eachErr                                 error
}

func (s *spyStore) ListContacts(ctx context.Context, p domain.Principal, slug string, opt domain.ReadOptions) (domain.Addressbook, []domain.Contact, error) {
	s.contacts = append(s.contacts, opt)
	return s.Store.ListContacts(ctx, p, slug, opt)
}

func (s *spyStore) ChangesSince(ctx context.Context, p domain.Principal, slug string, seq int64, opt domain.ReadOptions) (domain.Addressbook, []domain.Contact, []string, error) {
	s.changes = append(s.changes, opt)
	return s.Store.ChangesSince(ctx, p, slug, seq, opt)
}

func (s *spyStore) ListEvents(ctx context.Context, p domain.Principal, slug string, opt domain.ReadOptions) (domain.Calendar, []domain.Event, error) {
	s.events = append(s.events, opt)
	return s.Store.ListEvents(ctx, p, slug, opt)
}

func (s *spyStore) EventChangesSince(ctx context.Context, p domain.Principal, slug string, seq int64, opt domain.ReadOptions) (domain.Calendar, []domain.Event, []string, error) {
	s.eventChanges = append(s.eventChanges, opt)
	return s.Store.EventChangesSince(ctx, p, slug, seq, opt)
}

func (s *spyStore) EachContact(ctx context.Context, p domain.Principal, slug string, fn func(domain.Contact) (bool, error)) (domain.Addressbook, error) {
	if s.eachErr != nil {
		return domain.Addressbook{}, s.eachErr
	}
	return s.Store.EachContact(ctx, p, slug, fn)
}

func newHarnessWith(t *testing.T, mutate func(*app.Config)) (*harness, *spyStore) {
	t.Helper()
	cfg := testConfig
	mutate(&cfg)
	auth, store := apptest.NewAuth(ana, bea, cris), &spyStore{Store: apptest.NewStore()}
	uc, err := app.New(app.Deps{Auth: auth, Tenant: apptest.Binder{}, Store: store, Calendars: store, Logger: zap.NewNop(), Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(uc, Config{BasePath: base, Realm: "Contactos", MaxXMLBytes: 8 << 10, Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, h: h, auth: auth, store: store.Store}, store
}

func propfindBody(props ...string) string {
	return `<propfind xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><prop>` + strings.Join(props, "") + `</prop></propfind>`
}

// El prop de una peticion se repite en la respuesta de cada recurso: sin tope, un cuerpo de unos KiB pide
// decenas de miles de propiedades de cada contacto.
func TestUnPropConDemasiadasPropiedadesSeRechaza(t *testing.T) {
	hn := newHarness(t)
	hn.req("PROPFIND", homePath, "", "Depth", "1")
	hn.putCard("a.vcf", "a", "Ana")
	var props []string
	for i := 0; i <= maxRequestedProps; i++ {
		props = append(props, fmt.Sprintf(`<x%d/>`, i))
	}
	rec := hn.req("PROPFIND", bookPath, propfindBody(props...), "Depth", "1")
	expectStatus(t, rec, http.StatusBadRequest)

	sync := `<D:sync-collection xmlns:D="DAV:"><D:sync-token></D:sync-token><D:prop>` + strings.Join(props, "") + `</D:prop></D:sync-collection>`
	expectStatus(t, hn.req("REPORT", bookPath, sync), http.StatusBadRequest)
	multiget := `<C:addressbook-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:prop>` + strings.Join(props, "") + `</D:prop><D:href>` + bookPath + `a.vcf</D:href></C:addressbook-multiget>`
	expectStatus(t, hn.req("REPORT", bookPath, multiget), http.StatusBadRequest)

	hn.discover()
	calendarMultiget := `<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop>` + strings.Join(props, "") + `</D:prop><D:href>` + calPath + `a.ics</D:href></C:calendar-multiget>`
	expectStatus(t, hn.req("REPORT", calPath, calendarMultiget), http.StatusBadRequest)
}

// Pedir la misma propiedad varias veces no la repite en la respuesta: address-data pedido cien veces
// devolveria cien copias del vCard de cada contacto.
func TestLasPropiedadesRepetidasSeResponden1Vez(t *testing.T) {
	hn := newHarness(t)
	hn.req("PROPFIND", homePath, "", "Depth", "1")
	hn.putCard("a.vcf", "a", "Ana")
	many := strings.Repeat(`<C:address-data/>`, 60) + strings.Repeat(`<getetag/>`, 60)
	rec := hn.req("PROPFIND", bookPath, propfindBody(many), "Depth", "1")
	expectStatus(t, rec, http.StatusMultiStatus)
	// Una vez por recurso (el contacto la tiene y la libreta la responde como inexistente).
	for _, name := range []string{"<address-data", "<getetag"} {
		if n := strings.Count(rec.Body.String(), name); n != 2 {
			t.Fatalf("%s aparece %d veces en la respuesta de dos recursos", name, n)
		}
	}
}

// Un href repetido, o escrito de otra forma, se responde una vez: cada repeticion multiplicaria el
// contenido de la respuesta por lo que quepa en el cuerpo de la peticion.
func TestMulticontactoNoRepiteRecursos(t *testing.T) {
	hn, _ := newHarnessWith(t, func(c *app.Config) {
		c.Limits.MaxContactsPerMailbox, c.Calendar.MaxEventsPerMailbox = 200, 200
	})
	hn.req("PROPFIND", homePath, "", "Depth", "1")
	hn.putCard("a.vcf", "a", "Ana")
	hrefs := strings.Repeat("<d:href>"+bookPath+"a.vcf</d:href>", 90) + "<d:href>" + bookPath + "%61.vcf</d:href>"
	ms := parseMulti(t, hn.req("REPORT", bookPath, fmt.Sprintf(davx5Multiget, hrefs)))
	if len(ms.Responses) != 1 {
		t.Fatalf("un contacto pedido 91 veces se respondio %d veces", len(ms.Responses))
	}
	hn.discover()
	hn.putEvent("a.ics", simpleEvent("a", "Uno"))
	evHrefs := strings.Repeat("<d:href>"+calPath+"a.ics</d:href>", 90) + "<d:href>" + calPath + "%61.ics</d:href>"
	cal := parseMulti(t, hn.req("REPORT", calPath, fmt.Sprintf(davx5CalMultiget, evHrefs)))
	if len(cal.Responses) != 1 {
		t.Fatalf("un evento pedido 91 veces se respondio %d veces", len(cal.Responses))
	}
}

// Un listado que no pide el contenido de los objetos no lo lee: un PROPFIND de profundidad 1 sobre una
// libreta llena no puede cargar en memoria todos los vCard.
func TestLosListadosSinDatosNoLeenLosObjetos(t *testing.T) {
	hn, spy := newHarnessWith(t, func(*app.Config) {})
	hn.req("PROPFIND", homePath, "", "Depth", "1")
	hn.putCard("a.vcf", "a", "Ana")
	hn.discover()
	hn.putEvent("a.ics", simpleEvent("a", "Uno"))

	ms := parseMulti(t, hn.req("PROPFIND", bookPath, propfindBody("<getetag/>", "<getcontentlength/>"), "Depth", "1"))
	if size, _ := ms.prop(1, "getcontentlength"); size != fmt.Sprint(len(vcard("a", "Ana"))) {
		t.Fatalf("getcontentlength %q: sin leer el vCard se sigue informando su tamano", size)
	}
	parseMulti(t, hn.req("PROPFIND", bookPath, "", "Depth", "1"))
	parseMulti(t, hn.req("REPORT", bookPath, fmt.Sprintf(davx5Sync, ""), "Depth", "0"))
	parseMulti(t, hn.req("PROPFIND", calPath, propfindBody("<getetag/>"), "Depth", "1"))
	parseMulti(t, hn.req("REPORT", calPath, fmt.Sprintf(davx5CalSync, ""), "Depth", "0"))
	for name, opts := range map[string][]domain.ReadOptions{"contactos": spy.contacts, "eventos": spy.events} {
		if len(opts) == 0 {
			t.Fatalf("%s: ningun listado", name)
		}
		for _, o := range opts {
			if o.WithData {
				t.Fatalf("%s: un listado sin address-data ni calendar-data leyo los objetos", name)
			}
		}
	}

	spy.contacts, spy.events = nil, nil
	parseMulti(t, hn.req("PROPFIND", bookPath, propfindBody("<getetag/>", "<C:address-data/>"), "Depth", "1"))
	parseMulti(t, hn.req("PROPFIND", calPath, propfindBody("<getetag/>", `<C:calendar-data xmlns:C="urn:ietf:params:xml:ns:caldav"/>`), "Depth", "1"))
	if len(spy.contacts) != 1 || !spy.contacts[0].WithData || len(spy.events) != 1 || !spy.events[0].WithData {
		t.Fatalf("pidiendo los datos se deben leer: %+v %+v", spy.contacts, spy.events)
	}
	if spy.contacts[0].MaxBytes != testConfig.Limits.MaxReadBytes {
		t.Fatalf("la lectura no lleva el tope de bytes: %+v", spy.contacts[0])
	}
}

// Lo que una respuesta lleva de los objetos esta acotado: pasado el tope es un 507, no una respuesta de
// cientos de MiB armada en memoria.
func TestUnaRespuestaConDemasiadosDatosEsUn507(t *testing.T) {
	hn, _ := newHarnessWith(t, func(c *app.Config) { c.Limits.MaxReadBytes = 2 * len(vcard("a", "Ana")) })
	hn.req("PROPFIND", homePath, "", "Depth", "1")
	for _, n := range []string{"a", "b", "c"} {
		hn.putCard(n+".vcf", n, "Ana")
	}
	body := func(kind string) string {
		return `<C:addressbook-` + kind + ` xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:prop><D:getetag/><C:address-data/></D:prop>%s</C:addressbook-` + kind + `>`
	}
	get := fmt.Sprintf(body("multiget"), "<D:href>"+bookPath+"a.vcf</D:href><D:href>"+bookPath+"b.vcf</D:href><D:href>"+bookPath+"c.vcf</D:href>")
	rec := hn.req("REPORT", bookPath, get)
	expectStatus(t, rec, http.StatusInsufficientStorage)
	if !strings.Contains(rec.Body.String(), "number-of-matches-within-limits") {
		t.Fatalf("cuerpo: %s", rec.Body.String())
	}
	expectStatus(t, hn.req("REPORT", bookPath, fmt.Sprintf(body("query"), "")), http.StatusInsufficientStorage)
	// El mismo listado sin el contenido no lo lee y cabe siempre.
	ms := parseMulti(t, hn.req("PROPFIND", bookPath, propfindBody("<getetag/>"), "Depth", "1"))
	if len(ms.Responses) != 4 {
		t.Fatalf("sin datos: %v", ms.hrefs())
	}
	// Con una cota de resultados que cabe, la consulta responde.
	lim := fmt.Sprintf(body("query"), `<C:limit><C:nresults>2</C:nresults></C:limit>`)
	if ms := parseMulti(t, hn.req("REPORT", bookPath, lim)); len(ms.Responses) != 2 {
		t.Fatalf("nresults: %v", ms.hrefs())
	}
}

// Un buzon no guarda mas bytes de objetos que su espacio: sin ese tope, 10000 contactos de 256 KiB son
// 2,5 GiB por buzon en la base compartida.
func TestElEspacioDelBuzonEstaAcotado(t *testing.T) {
	size := len(vcard("a", "Ana"))
	hn, _ := newHarnessWith(t, func(c *app.Config) { c.Limits.MaxMailboxBytes = int64(2*size + size/2) })
	hn.req("PROPFIND", homePath, "", "Depth", "1")
	expectStatus(t, hn.putCard("a.vcf", "a", "Ana"), http.StatusCreated)
	expectStatus(t, hn.putCard("b.vcf", "b", "Ana"), http.StatusCreated)
	rec := hn.putCard("c.vcf", "c", "Ana")
	expectStatus(t, rec, http.StatusInsufficientStorage)
	if !strings.Contains(rec.Body.String(), "quota-not-exceeded") {
		t.Fatalf("cuerpo: %s", rec.Body.String())
	}
	// Crecer un contacto que ya estaba tambien cuenta; achicarlo no.
	expectStatus(t, hn.req("PUT", bookPath+"a.vcf", strings.Replace(vcard("a", "Ana"), "FN:Ana", "FN:"+strings.Repeat("x", size), 1), "Content-Type", textCard), http.StatusInsufficientStorage)
	expectStatus(t, hn.req("DELETE", bookPath+"b.vcf", ""), http.StatusNoContent)
	expectStatus(t, hn.putCard("c.vcf", "c", "Ana"), http.StatusCreated)

	hn.discover()
	ev := simpleEvent("a", "Uno")
	hn2, _ := newHarnessWith(t, func(c *app.Config) { c.Limits.MaxMailboxBytes = int64(len(ev) + len(ev)/2) })
	hn2.discover()
	expectStatus(t, hn2.putEvent("a.ics", ev), http.StatusCreated)
	expectStatus(t, hn2.putEvent("b.ics", simpleEvent("b", "Dos")), http.StatusInsufficientStorage)
}

// Una consulta que se cancela o vence responde 503 con Retry-After, no un 500 con el error de la base.
func TestUnaConsultaVencidaEsUn503(t *testing.T) {
	hn, spy := newHarnessWith(t, func(*app.Config) {})
	hn.req("PROPFIND", homePath, "", "Depth", "1")
	spy.eachErr = context.DeadlineExceeded
	q := `<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:prop><D:getetag/></D:prop></C:addressbook-query>`
	rec := hn.req("REPORT", bookPath, q)
	expectStatus(t, rec, http.StatusServiceUnavailable)
	if rec.Header().Get("Retry-After") == "" || strings.Contains(rec.Body.String(), "deadline") {
		t.Fatalf("respuesta: %v %q", rec.Header(), rec.Body.String())
	}
}

// XML patologico del tamano maximo de un cuerpo: anidamiento de decenas de miles de niveles, cientos de miles de
// elementos hermanos, miles de atributos y de declaraciones de espacios de nombres. encoding/xml los recorre
// sin recursion y sin acumularlos: la respuesta llega en milisegundos y nunca es un fallo interno.
func TestXMLPatologicoDelTamanoMaximoSeAtiendeEnMilisegundos(t *testing.T) {
	hn := newHarness(t)
	hn.h.cfg.MaxXMLBytes = 256 << 10
	hn.req("PROPFIND", homePath, "", "Depth", "1")
	for name, body := range map[string]string{
		"anidado":    `<propfind xmlns="DAV:"><prop><x>` + strings.Repeat("<a>", 30000) + strings.Repeat("</a>", 30000) + `</x></prop></propfind>`,
		"hermanos":   `<propfind xmlns="DAV:"><prop><x>` + strings.Repeat("<a b='c'/>", 20000) + `</x></prop></propfind>`,
		"atributos":  `<propfind xmlns="DAV:"><prop><x ` + strings.Repeat(`a="1" `, 30000) + `/></prop></propfind>`,
		"espacios":   `<propfind xmlns="DAV:"><prop>` + strings.Repeat(`<x xmlns:p1="u" xmlns:p2="v" xmlns:p3="w"/>`, 5000) + `</prop></propfind>`,
		"sin cerrar": `<propfind xmlns="DAV:"><prop>` + strings.Repeat("<a>", 30000),
	} {
		start := time.Now()
		rec := hn.req("PROPFIND", bookPath, body, "Depth", "0")
		if rec.Code >= 500 || time.Since(start) > 2*time.Second {
			t.Errorf("%s: %d en %v", name, rec.Code, time.Since(start))
		}
	}
}
