package http

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
)

const (
	calHome = base + "/calendars/ana@acme.test/"
	calPath = calHome + "calendar/"
	textCal = "text/calendar; charset=utf-8"
)

// ics arma un iCalendar con un VEVENT de lo que se le da (cada propiedad en su linea).
func ics(uid string, props ...string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Prueba//ES\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:20260101T000000Z\r\n" +
		strings.Join(props, "\r\n") + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

func simpleEvent(uid, summary string) string {
	return ics(uid, "SUMMARY:"+summary, "DTSTART:20260921T100000Z", "DTEND:20260921T110000Z")
}

func (hn *harness) putEvent(res, body string, headers ...string) *httptest.ResponseRecorder {
	hn.t.Helper()
	return hn.req("PUT", calPath+res, body, append([]string{"Content-Type", textCal}, headers...)...)
}

// discover crea el calendario por defecto como lo hace un cliente al listar su home.
func (hn *harness) discover() {
	hn.t.Helper()
	expectStatus(hn.t, hn.req("PROPFIND", calHome, "", "Depth", "1"), http.StatusMultiStatus)
}

// Cuerpos de peticion como los envian los clientes reales, escritos a mano segun RFC 4791, 5689 y 6578.
const (
	davx5CalHomeSet = `<?xml version="1.0" encoding="utf-8" ?>
<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><c:calendar-home-set/></d:prop></d:propfind>`
	davx5Calendars = `<?xml version="1.0" encoding="utf-8" ?>
<d:propfind xmlns:d="DAV:" xmlns:cs="http://calendarserver.org/ns/" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop>` +
		`<d:resourcetype/><d:displayname/><d:current-user-privilege-set/><cs:getctag/><c:calendar-description/><c:supported-calendar-component-set/><d:sync-token/><a:calendar-color xmlns:a="http://apple.com/ns/ical/"/>` +
		`</d:prop></d:propfind>`
	davx5CalSync = `<?xml version="1.0" encoding="utf-8" ?>
<d:sync-collection xmlns:d="DAV:"><d:sync-token>%s</d:sync-token><d:sync-level>1</d:sync-level><d:prop><d:getcontenttype/><d:getetag/></d:prop></d:sync-collection>`
	davx5CalMultiget = `<?xml version="1.0" encoding="utf-8" ?>
<c:calendar-multiget xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><d:getetag/><c:calendar-data/></d:prop>%s</c:calendar-multiget>`

	// iOS: prefijos A y C, propiedades que este servidor no conoce y consulta con rango de tiempo.
	iosCalendar = `<?xml version="1.0" encoding="UTF-8"?>
<A:propfind xmlns:A="DAV:"><A:prop><A:add-member/><C:schedule-inbox-URL xmlns:C="urn:ietf:params:xml:ns:caldav"/>` +
		`<D:getctag xmlns:D="http://calendarserver.org/ns/"/><A:displayname/><A:resourcetype/><A:sync-token/><A:supported-report-set/>` +
		`<C:supported-calendar-component-set xmlns:C="urn:ietf:params:xml:ns:caldav"/><C:max-resource-size xmlns:C="urn:ietf:params:xml:ns:caldav"/></A:prop></A:propfind>`
	iosMkcalendar = `<?xml version="1.0" encoding="UTF-8"?>
<B:mkcalendar xmlns:B="urn:ietf:params:xml:ns:caldav"><A:set xmlns:A="DAV:"><A:prop>` +
		`<A:displayname>Trabajo</A:displayname><D:calendar-color xmlns:D="http://apple.com/ns/ical/">#1BADF8FF</D:calendar-color>` +
		`<D:calendar-order xmlns:D="http://apple.com/ns/ical/">3</D:calendar-order>` +
		`<B:supported-calendar-component-set><B:comp name="VEVENT"/></B:supported-calendar-component-set>` +
		`<B:calendar-description>Reuniones</B:calendar-description></A:prop></A:set></B:mkcalendar>`

	// Thunderbird: consulta por rango sobre VEVENT.
	thunderbirdRange = `<?xml version="1.0" encoding="UTF-8"?>
<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:"><D:prop><D:getetag/><C:calendar-data/></D:prop>` +
		`<C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"><C:time-range start="%s" end="%s"/></C:comp-filter></C:comp-filter></C:filter></C:calendar-query>`
	calQueryWith = `<?xml version="1.0" encoding="UTF-8"?>
<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:"><D:prop><D:getetag/></D:prop>` +
		`<C:filter><C:comp-filter name="VCALENDAR">%s</C:comp-filter></C:filter></C:calendar-query>`
)

func vevents(inner string) string {
	return `<C:comp-filter name="VEVENT">` + inner + `</C:comp-filter>`
}

func TestOptionsAnunciaCalDAV(t *testing.T) {
	hn := newHarness(t)
	rec := hn.do(request{method: "OPTIONS", path: calPath, noAuth: true})
	expectStatus(t, rec, http.StatusOK)
	if dav := rec.Header().Get("DAV"); !strings.Contains(dav, "calendar-access") || !strings.Contains(dav, "addressbook") {
		t.Fatalf("DAV: %q", dav)
	}
	if !strings.Contains(rec.Header().Get("Allow"), "MKCALENDAR") {
		t.Fatalf("Allow: %q", rec.Header().Get("Allow"))
	}
}

func TestDescubrimientoDeCalendariosComoDAVx5(t *testing.T) {
	hn := newHarness(t)
	ms := parseMulti(t, hn.req("PROPFIND", base+"/principals/ana@acme.test/", davx5CalHomeSet, "Depth", "0"))
	if home, ok := ms.prop(0, "calendar-home-set"); !ok || home != calHome {
		t.Fatalf("calendar-home-set: %q %v", home, ok)
	}
	ms = parseMulti(t, hn.req("PROPFIND", calHome, davx5Calendars, "Depth", "1"))
	if len(ms.Responses) != 2 || ms.Responses[0].Href != calHome || ms.Responses[1].Href != calPath {
		t.Fatalf("home + calendario por defecto: %v", ms.hrefs())
	}
	if name, _ := ms.prop(1, "displayname"); name != "Calendario" {
		t.Fatalf("displayname: %q", name)
	}
	if comp, ok := ms.prop(1, "supported-calendar-component-set"); !ok || comp != "" {
		t.Fatalf("supported-calendar-component-set: %q %v", comp, ok)
	}
	if !ms.missing(1, "calendar-color") {
		t.Fatal("el color no se guarda: va en un propstat 404")
	}
	body := hn.req("PROPFIND", calHome, davx5Calendars, "Depth", "1").Body.String()
	for _, want := range []string{`<calendar xmlns="urn:ietf:params:xml:ns:caldav">`, `name="VEVENT"`, "collection", "getctag", "urn:mail-dav:sync:"} {
		if !strings.Contains(body, want) {
			t.Errorf("la respuesta no menciona %q:\n%s", want, body)
		}
	}
	// Un segundo descubrimiento no crea otro calendario.
	if again := parseMulti(t, hn.req("PROPFIND", calHome, davx5Calendars, "Depth", "1")); len(again.Responses) != 2 {
		t.Fatalf("segundo descubrimiento: %v", again.hrefs())
	}
}

func TestElPrincipalOfreceLasDosCasasYElAllpropLasLista(t *testing.T) {
	hn := newHarness(t)
	all := parseMulti(t, hn.req("PROPFIND", base+"/principals/ana@acme.test/", "", "Depth", "0"))
	if h, _ := all.prop(0, "addressbook-home-set"); h != homePath {
		t.Fatalf("addressbook-home-set: %q", h)
	}
	if h, _ := all.prop(0, "calendar-home-set"); h != calHome {
		t.Fatalf("calendar-home-set: %q", h)
	}
}

func TestPropfindDeUnCalendarioComoIOSYThunderbird(t *testing.T) {
	hn := newHarness(t)
	hn.discover()
	ms := parseMulti(t, hn.req("PROPFIND", calPath, iosCalendar, "Depth", "0"))
	for _, name := range []string{"add-member", "schedule-inbox-URL"} {
		if !ms.missing(0, name) {
			t.Errorf("%s no esta soportada y debe ir en un propstat 404", name)
		}
	}
	if size, ok := ms.prop(0, "max-resource-size"); !ok || size != fmt.Sprint(calendarLimits.MaxEventBytes) {
		t.Fatalf("max-resource-size: %q %v", size, ok)
	}
	body := hn.req("PROPFIND", calPath, iosCalendar, "Depth", "0").Body.String()
	for _, want := range []string{"calendar-query", "calendar-multiget", "sync-collection"} {
		if !strings.Contains(body, want) {
			t.Errorf("supported-report-set sin %s", want)
		}
	}
	if strings.Contains(body, "addressbook-query") {
		t.Error("un calendario no anuncia informes de libretas")
	}

	hn.putEvent("uno.ics", simpleEvent("uno", "Uno"))
	list := parseMulti(t, hn.req("PROPFIND", calPath, `<propfind xmlns="DAV:"><prop><getetag/><getcontenttype/></prop></propfind>`, "Depth", "1"))
	if got := list.hrefs(); len(got) != 2 || got[1] != calPath+"uno.ics" {
		t.Fatalf("listado: %v", got)
	}
	if ct, _ := list.prop(1, "getcontenttype"); !strings.HasPrefix(ct, "text/calendar") {
		t.Fatalf("getcontenttype: %q", ct)
	}
	if _, ok := list.prop(1, "calendar-data"); ok {
		t.Fatal("el contenido solo se devuelve si se pide")
	}
	one := parseMulti(t, hn.req("PROPFIND", calPath+"uno.ics", `<propfind xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><prop><C:calendar-data/></prop></propfind>`, "Depth", "0"))
	if data, ok := one.prop(0, "calendar-data"); !ok || data != simpleEvent("uno", "Uno") {
		t.Fatalf("calendar-data: %q %v", data, ok)
	}
	if hn.req("PROPFIND", calPath, "", "Depth", "infinity").Code != http.StatusForbidden {
		t.Fatal("profundidad infinita")
	}
}

func TestCicloDeUnEventoConEtags(t *testing.T) {
	hn := newHarness(t)
	hn.discover()
	rec := hn.putEvent("uno.ics", simpleEvent("uno", "Uno"), "If-None-Match", "*")
	expectStatus(t, rec, http.StatusCreated)
	etag := rec.Header().Get("ETag")
	if want := `"` + domain.ETagOf(simpleEvent("uno", "Uno")) + `"`; etag != want {
		t.Fatalf("ETag %q, quiero %q", etag, want)
	}
	get := hn.req("GET", calPath+"uno.ics", "")
	expectStatus(t, get, http.StatusOK)
	if get.Body.String() != simpleEvent("uno", "Uno") || get.Header().Get("ETag") != etag || !strings.HasPrefix(get.Header().Get("Content-Type"), "text/calendar") {
		t.Fatalf("GET: %v %q", get.Header(), get.Body.String())
	}
	head := hn.req("HEAD", calPath+"uno.ics", "")
	if head.Body.Len() != 0 || head.Header().Get("Content-Length") == "" {
		t.Fatalf("HEAD: %d bytes", head.Body.Len())
	}
	expectStatus(t, hn.req("GET", calPath+"uno.ics", "", "If-None-Match", etag), http.StatusNotModified)

	expectStatus(t, hn.putEvent("uno.ics", simpleEvent("uno", "Otra vez"), "If-None-Match", "*"), http.StatusPreconditionFailed)
	expectStatus(t, hn.putEvent("uno.ics", simpleEvent("uno", "Con etag viejo"), "If-Match", `"deadbeef"`), http.StatusPreconditionFailed)
	rec = hn.putEvent("uno.ics", simpleEvent("uno", "Editado"), "If-Match", etag)
	expectStatus(t, rec, http.StatusNoContent)
	if rec.Header().Get("ETag") == etag {
		t.Fatal("el ETag cambia con el contenido")
	}
	expectStatus(t, hn.req("DELETE", calPath+"uno.ics", "", "If-Match", etag), http.StatusPreconditionFailed)
	expectStatus(t, hn.req("DELETE", calPath+"uno.ics", "", "If-Match", rec.Header().Get("ETag")), http.StatusNoContent)
	expectStatus(t, hn.req("GET", calPath+"uno.ics", ""), http.StatusNotFound)
	expectStatus(t, hn.req("DELETE", calPath+"uno.ics", ""), http.StatusNotFound)
	// Guardar lo mismo no es un cambio.
	hn.putEvent("dos.ics", simpleEvent("dos", "Dos"))
	before, _ := hn.store.GetCalendar(nil, ana.Principal, "calendar")
	expectStatus(t, hn.putEvent("dos.ics", simpleEvent("dos", "Dos")), http.StatusNoContent)
	if after, _ := hn.store.GetCalendar(nil, ana.Principal, "calendar"); after.SyncSeq != before.SyncSeq {
		t.Fatalf("reenviar el mismo contenido no es un cambio: %d -> %d", before.SyncSeq, after.SyncSeq)
	}
}

func TestPutEventoValidaContenidoTipoYTamano(t *testing.T) {
	hn := newHarness(t)
	hn.discover()
	put := func(body string, headers ...string) *httptest.ResponseRecorder {
		return hn.req("PUT", calPath+"x.ics", body, headers...)
	}
	expectStatus(t, put(simpleEvent("a", "A"), "Content-Type", "text/vcard"), http.StatusUnsupportedMediaType)
	rec := put(simpleEvent("a", "A"), "Content-Type", "text/calendar; charset=iso-8859-1")
	expectStatus(t, rec, http.StatusUnsupportedMediaType)
	if !strings.Contains(rec.Body.String(), "supported-calendar-data") {
		t.Fatalf("cuerpo: %s", rec.Body.String())
	}
	for i, ct := range []string{"text/calendar", "text/calendar; charset=UTF-8; component=vevent", ""} {
		expectStatus(t, hn.req("PUT", fmt.Sprintf("%st%d.ics", calPath, i), simpleEvent(fmt.Sprintf("t%d", i), "T"), "Content-Type", ct), http.StatusCreated)
	}
	for name, tc := range map[string]struct{ body, want string }{
		"no es iCalendar":  {"<html>no soy un evento</html>", "valid-calendar-data"},
		"sin UID":          {ics("", "DTSTART:20260921T100000Z"), "valid-calendar-data"},
		"sin DTSTART":      {ics("sin-inicio", "SUMMARY:x"), "valid-calendar-data"},
		"con METHOD":       {strings.Replace(simpleEvent("m", "M"), "VERSION:2.0", "VERSION:2.0\r\nMETHOD:REQUEST", 1), "valid-calendar-object-resource"},
		"una tarea":        {"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VTODO\r\nUID:t\r\nEND:VTODO\r\nEND:VCALENDAR\r\n", "supported-calendar-component"},
		"sin eventos":      {"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nEND:VCALENDAR\r\n", "valid-calendar-object-resource"},
		"demasiado grande": {simpleEvent("big", strings.Repeat("A", calendarLimits.MaxEventBytes)), "max-resource-size"},
	} {
		rec := put(tc.body, "Content-Type", textCal)
		expectStatus(t, rec, http.StatusForbidden)
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: %s", name, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "soy un evento") {
			t.Errorf("%s: el cuerpo repite la entrada", name)
		}
	}
	// Un cuerpo enorme se corta al llegar al tope, sin leerlo entero.
	huge := &countingReader{remaining: 50 << 20}
	req := httptest.NewRequest("PUT", "http://mail.acme.test"+calPath+"huge.ics", huge)
	req.SetBasicAuth(ana.Principal.Username, ana.Password)
	req.Header.Set("Content-Type", textCal)
	rec = httptest.NewRecorder()
	hn.h.ServeHTTP(rec, req)
	expectStatus(t, rec, http.StatusForbidden)
	if !strings.Contains(rec.Body.String(), "max-resource-size") || huge.read > int64(calendarLimits.MaxEventBytes)+(64<<10) {
		t.Fatalf("se leyeron %d bytes de un cuerpo que debia cortarse en %d: %s", huge.read, calendarLimits.MaxEventBytes, rec.Body.String())
	}
	if _, err := hn.store.GetEvent(nil, ana.Principal, "calendar", "huge.ics"); err == nil {
		t.Fatal("un evento rechazado no se guarda")
	}
	expectStatus(t, hn.req("PUT", calPath+"a.txt", simpleEvent("a", "A"), "Content-Type", textCal), http.StatusForbidden)
	expectStatus(t, hn.req("PUT", calPath+"a.vcf", simpleEvent("a", "A"), "Content-Type", textCal), http.StatusForbidden)
}

func TestPutEventoConflictosYLimites(t *testing.T) {
	hn := newHarness(t)
	expectStatus(t, hn.req("PUT", calHome+"noexiste/x.ics", simpleEvent("a", "A"), "Content-Type", textCal), http.StatusConflict)
	hn.discover()
	hn.putEvent("a.ics", simpleEvent("mismo-uid", "A"))
	rec := hn.putEvent("b.ics", simpleEvent("mismo-uid", "B"))
	expectStatus(t, rec, http.StatusConflict)
	if !strings.Contains(rec.Body.String(), "no-uid-conflict") || !strings.Contains(rec.Body.String(), calPath+"a.ics") {
		t.Fatalf("no-uid-conflict debe nombrar el recurso: %s", rec.Body.String())
	}
	for i := 0; i < calendarLimits.MaxEventsPerMailbox-1; i++ {
		expectStatus(t, hn.putEvent(fmt.Sprintf("e%d.ics", i), simpleEvent(fmt.Sprintf("e%d", i), "E")), http.StatusCreated)
	}
	rec = hn.putEvent("extra.ics", simpleEvent("extra", "Extra"))
	expectStatus(t, rec, http.StatusInsufficientStorage)
	if !strings.Contains(rec.Body.String(), "quota-not-exceeded") {
		t.Fatalf("cuerpo: %s", rec.Body.String())
	}
	expectStatus(t, hn.putEvent("a.ics", simpleEvent("mismo-uid", "A editada")), http.StatusNoContent)
}

func TestPutYGetNoAdmitenRutasQueNoSonEventos(t *testing.T) {
	hn := newHarness(t)
	hn.discover()
	for _, path := range []string{calPath, calHome, base + "/calendars/"} {
		expectStatus(t, hn.req("PUT", path, simpleEvent("a", "A"), "Content-Type", textCal), http.StatusMethodNotAllowed)
		expectStatus(t, hn.req("GET", path, ""), http.StatusMethodNotAllowed)
	}
	for _, path := range []string{calHome, base + "/calendars/"} {
		expectStatus(t, hn.req("DELETE", path, ""), http.StatusForbidden)
	}
}

func TestConsultaPorRangoComoThunderbird(t *testing.T) {
	hn := newHarness(t)
	hn.discover()
	hn.putEvent("suelto.ics", simpleEvent("suelto", "Suelto"))
	hn.putEvent("semanal.ics", ics("semanal", "SUMMARY:Semanal", "DTSTART:20260105T100000Z", "DTEND:20260105T110000Z", "RRULE:FREQ=WEEKLY;BYDAY=MO", "EXDATE:20260928T100000Z"))
	hn.putEvent("lejano.ics", ics("lejano", "SUMMARY:Lejano", "DTSTART:20270101T100000Z", "DTEND:20270101T110000Z"))
	hn.putEvent("acabado.ics", ics("acabado", "SUMMARY:Acabado", "DTSTART:20260105T100000Z", "DTEND:20260105T110000Z", "RRULE:FREQ=WEEKLY;COUNT=3"))
	hn.putEvent("dia.ics", ics("dia", "SUMMARY:Cumple", "DTSTART;VALUE=DATE:20260921"))
	hn.putEvent("madrid.ics", ics("madrid", "SUMMARY:En Madrid", "DTSTART;TZID=Europe/Madrid:20260921T010000", "DTEND;TZID=Europe/Madrid:20260921T020000"))

	query := func(start, end string) multiResp {
		return parseMulti(t, hn.req("REPORT", calPath, fmt.Sprintf(thunderbirdRange, start, end), "Depth", "1"))
	}
	names := func(ms multiResp) string {
		out := make([]string, len(ms.Responses))
		for i, h := range ms.hrefs() {
			out[i] = strings.TrimPrefix(h, calPath)
		}
		return strings.Join(out, ",")
	}
	// El lunes 21 de septiembre de 2026 UTC: el evento suelto, la serie semanal, el de dia completo y el de
	// Madrid (01:00 hora de Madrid es 23:00 UTC del dia 20: cae fuera).
	ms := query("20260921T000000Z", "20260922T000000Z")
	if got := names(ms); got != "dia.ics,semanal.ics,suelto.ics" {
		t.Fatalf("21 de septiembre: %s", got)
	}
	if data, ok := ms.prop(2, "calendar-data"); !ok || data != simpleEvent("suelto", "Suelto") {
		t.Fatalf("calendar-data: %q", data)
	}
	// Con el rango en hora de Madrid el evento de las 01:00 si entra (y el de dia completo, que empieza a las 00:00 UTC).
	if got := names(query("20260920T210000Z", "20260921T003000Z")); got != "dia.ics,madrid.ics" {
		t.Fatalf("madrid: %s", got)
	}
	// Un martes: la semanal no cae.
	if got := names(query("20260922T000000Z", "20260923T000000Z")); got != "" {
		t.Fatalf("martes: %q", got)
	}
	// El lunes excluido por EXDATE.
	if got := names(query("20260928T000000Z", "20260929T000000Z")); got != "" {
		t.Fatalf("lunes con EXDATE: %q", got)
	}
	if got := names(query("20261005T000000Z", "20261006T000000Z")); got != "semanal.ics" {
		t.Fatalf("lunes siguiente: %q", got)
	}
	// La serie con COUNT=3 termina el 19 de enero, y el evento lejano solo aparece en 2027.
	if got := names(query("20260126T000000Z", "20260127T000000Z")); got != "semanal.ics" {
		t.Fatalf("despues del COUNT: %q", got)
	}
	if got := names(query("20270101T000000Z", "20270102T000000Z")); got != "lejano.ics" {
		t.Fatalf("2027 (un viernes: la semanal no cae): %q", got)
	}
	if got := names(query("20270104T000000Z", "20270105T000000Z")); got != "semanal.ics" {
		t.Fatalf("2027 (un lunes): %q", got)
	}
	// Solo un extremo.
	only := parseMulti(t, hn.req("REPORT", calPath, fmt.Sprintf(calQueryWith, vevents(`<C:time-range start="20261231T000000Z"/>`))))
	if got := names(only); got != "lejano.ics,semanal.ics" {
		t.Fatalf("solo start: %q", got)
	}
}

func TestConsultaPorTextoYComponentes(t *testing.T) {
	hn := newHarness(t)
	hn.discover()
	hn.putEvent("a.ics", ics("a", "SUMMARY:Reunion de Presupuesto", "DTSTART:20260921T100000Z", "DTEND:20260921T110000Z", "LOCATION:Sala 3"))
	hn.putEvent("b.ics", ics("b", "SUMMARY:Almuerzo", "DTSTART:20260921T120000Z", "DTEND:20260921T130000Z"))
	q := func(inner string) []string {
		return parseMulti(t, hn.req("REPORT", calPath, fmt.Sprintf(calQueryWith, inner))).hrefs()
	}
	if got := q(""); len(got) != 2 {
		t.Fatalf("solo VCALENDAR devuelve todo: %v", got)
	}
	if got := q(vevents(`<C:prop-filter name="SUMMARY"><C:text-match collation="i;ascii-casemap">presupuesto</C:text-match></C:prop-filter>`)); len(got) != 1 || got[0] != calPath+"a.ics" {
		t.Fatalf("text-match: %v", got)
	}
	if got := q(vevents(`<C:prop-filter name="SUMMARY"><C:text-match negate-condition="yes">presupuesto</C:text-match></C:prop-filter>`)); len(got) != 1 || got[0] != calPath+"b.ics" {
		t.Fatalf("negado: %v", got)
	}
	if got := q(vevents(`<C:prop-filter name="LOCATION"><C:is-not-defined/></C:prop-filter>`)); len(got) != 1 || got[0] != calPath+"b.ics" {
		t.Fatalf("is-not-defined: %v", got)
	}
	if got := q(vevents(`<C:prop-filter name="SUMMARY" test="allof"><C:text-match>reunion</C:text-match><C:text-match>almuerzo</C:text-match></C:prop-filter>`)); len(got) != 0 {
		t.Fatalf("allof: %v", got)
	}
	// Las tareas: la coleccion solo guarda eventos, asi que no hay ninguna.
	if got := q(`<C:comp-filter name="VTODO"/>`); len(got) != 0 {
		t.Fatalf("VTODO: %v", got)
	}
	if got := q(`<C:comp-filter name="VTODO"><C:is-not-defined/></C:comp-filter>`); len(got) != 2 {
		t.Fatalf("VTODO is-not-defined: %v", got)
	}
	if got := q(vevents(`<C:is-not-defined/>`)); len(got) != 0 {
		t.Fatalf("VEVENT is-not-defined: %v", got)
	}
}

func TestConsultasConFiltrosQueNoSeSabenEvaluar(t *testing.T) {
	hn := newHarness(t)
	hn.discover()
	hn.putEvent("a.ics", simpleEvent("a", "A"))
	report := func(body string) *httptest.ResponseRecorder { return hn.req("REPORT", calPath, body) }
	for name, tc := range map[string]struct{ inner, want string }{
		"param-filter":            {vevents(`<C:prop-filter name="ATTENDEE"><C:param-filter name="PARTSTAT"/></C:prop-filter>`), "supported-filter"},
		"time-range en propiedad": {vevents(`<C:prop-filter name="DTSTAMP"><C:time-range start="20260101T000000Z"/></C:prop-filter>`), "supported-filter"},
		"comp-filter de alarma":   {vevents(`<C:comp-filter name="VALARM"/>`), "supported-filter"},
		"componente desconocido":  {`<C:comp-filter name="VTIMEZONE"/>`, "supported-filter"},
		"elemento desconocido":    {vevents(`<C:algo-nuevo/>`), "supported-filter"},
		"propiedad sin nombre":    {vevents(`<C:prop-filter/>`), "supported-filter"},
		"colacion desconocida":    {vevents(`<C:prop-filter name="SUMMARY"><C:text-match collation="i;klingon">x</C:text-match></C:prop-filter>`), "supported-collation"},
		"tipo de comparacion":     {vevents(`<C:prop-filter name="SUMMARY"><C:text-match match-type="regex">x</C:text-match></C:prop-filter>`), "supported-filter"},
	} {
		rec := report(fmt.Sprintf(calQueryWith, tc.inner))
		expectStatus(t, rec, http.StatusForbidden)
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: %s", name, rec.Body.String())
		}
	}
	notCalendar := `<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:"><D:prop><D:getetag/></D:prop><C:filter><C:comp-filter name="VEVENT"/></C:filter></C:calendar-query>`
	expectStatus(t, report(notCalendar), http.StatusForbidden)
	two := `<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:"><D:prop><D:getetag/></D:prop><C:filter><C:comp-filter name="VCALENDAR"/><C:comp-filter name="VCALENDAR"/></C:filter></C:calendar-query>`
	expectStatus(t, report(two), http.StatusForbidden)
	// Modificadores de calendar-data que piden expandir recurrencias en el servidor.
	expand := `<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:"><D:prop><D:getetag/><C:calendar-data><C:expand start="20260101T000000Z" end="20270101T000000Z"/></C:calendar-data></D:prop>` +
		`<C:filter><C:comp-filter name="VCALENDAR"/></C:filter></C:calendar-query>`
	rec := report(expand)
	expectStatus(t, rec, http.StatusForbidden)
	if !strings.Contains(rec.Body.String(), "supported-calendar-data") {
		t.Fatalf("expand: %s", rec.Body.String())
	}
	// La recuperacion parcial (comp) no se aplica: se devuelve el objeto entero.
	partial := `<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:"><D:prop><C:calendar-data><C:comp name="VCALENDAR"><C:prop name="VERSION"/></C:comp></C:calendar-data></D:prop>` +
		`<C:filter><C:comp-filter name="VCALENDAR"/></C:filter></C:calendar-query>`
	ms := parseMulti(t, report(partial))
	if data, _ := ms.prop(0, "calendar-data"); data != simpleEvent("a", "A") {
		t.Fatalf("calendar-data completo: %q", data)
	}
	// Cuerpos mal formados.
	for name, body := range map[string]string{
		"sin filtro":              `<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:"><D:prop><D:getetag/></D:prop></C:calendar-query>`,
		"filtro vacio":            fmt.Sprintf(`<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:"><C:filter/></C:calendar-query>`),
		"time-range sin extremos": fmt.Sprintf(calQueryWith, vevents(`<C:time-range/>`)),
		"time-range no UTC":       fmt.Sprintf(calQueryWith, vevents(`<C:time-range start="20260101T000000"/>`)),
		"time-range al reves":     fmt.Sprintf(calQueryWith, vevents(`<C:time-range start="20270101T000000Z" end="20260101T000000Z"/>`)),
		"time-range con basura":   fmt.Sprintf(calQueryWith, vevents(`<C:time-range start="manana"/>`)),
		"is-not-defined con mas":  fmt.Sprintf(calQueryWith, vevents(`<C:is-not-defined/><C:time-range start="20260101T000000Z"/>`)),
		"elemento suelto":         `<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:"><C:otro/><C:filter><C:comp-filter name="VCALENDAR"/></C:filter></C:calendar-query>`,
		"cuerpo vacio":            "",
		"xml roto":                "<C:calendar-query",
	} {
		if rec := report(body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	// Informes de otro tipo o sobre otro recurso.
	expectStatus(t, report(`<D:principal-match xmlns:D="DAV:"/>`), http.StatusForbidden)
	expectStatus(t, report(`<C:free-busy-query xmlns:C="urn:ietf:params:xml:ns:caldav"/>`), http.StatusForbidden)
	expectStatus(t, hn.req("REPORT", calHome, fmt.Sprintf(calQueryWith, "")), http.StatusForbidden)
	// Una zona horaria en el cuerpo (horas flotantes) es admisible: se toman como UTC.
	withTZ := `<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:"><D:prop><D:getetag/></D:prop><C:filter><C:comp-filter name="VCALENDAR"/></C:filter><C:timezone>BEGIN:VCALENDAR</C:timezone></C:calendar-query>`
	expectStatus(t, report(withTZ), http.StatusMultiStatus)
}

func TestMulticalendarioYSincronizacionComoDAVx5(t *testing.T) {
	hn := newHarness(t)
	ms := parseMulti(t, hn.req("PROPFIND", calHome, davx5Calendars, "Depth", "1"))
	initial, _ := ms.prop(1, "sync-token")
	sync := func(token string) multiResp {
		return parseMulti(t, hn.req("REPORT", calPath, fmt.Sprintf(davx5CalSync, token), "Depth", "0"))
	}
	first := sync("")
	if len(first.Responses) != 0 || first.SyncToken != initial {
		t.Fatalf("calendario vacio: %+v (token %q, esperaba %q)", first.hrefs(), first.SyncToken, initial)
	}
	hn.putEvent("a.ics", simpleEvent("a", "Ana"))
	hn.putEvent("b.ics", simpleEvent("b", "Beto"))
	next := sync(first.SyncToken)
	if got := next.hrefs(); len(got) != 2 || got[0] != calPath+"a.ics" || got[1] != calPath+"b.ics" {
		t.Fatalf("cambios: %v", got)
	}
	if etag, ok := next.prop(0, "getetag"); !ok || etag != `"`+domain.ETagOf(simpleEvent("a", "Ana"))+`"` {
		t.Fatalf("getetag: %q", etag)
	}
	if ct, _ := next.prop(0, "getcontenttype"); !strings.HasPrefix(ct, "text/calendar") {
		t.Fatalf("getcontenttype: %q", ct)
	}
	if idle := sync(next.SyncToken); len(idle.Responses) != 0 || idle.SyncToken != next.SyncToken {
		t.Fatalf("sin cambios: %v", idle.hrefs())
	}
	hn.putEvent("a.ics", simpleEvent("a", "Ana editada"))
	hn.req("DELETE", calPath+"b.ics", "")
	delta := sync(next.SyncToken)
	if len(delta.Responses) != 2 || delta.Responses[0].Href != calPath+"a.ics" || delta.Responses[1].Href != calPath+"b.ics" ||
		!strings.Contains(delta.Responses[1].Status, "404") {
		t.Fatalf("edicion y borrado: %+v", delta.hrefs())
	}

	get := fmt.Sprintf(davx5CalMultiget, "<d:href>"+calPath+"a.ics</d:href><d:href>"+calPath+"b.ics</d:href><d:href>"+calPath+"../a.ics</d:href><d:href>"+base+"/addressbooks/ana@acme.test/contacts/a.vcf</d:href>")
	mg := parseMulti(t, hn.req("REPORT", calPath, get, "Depth", "1"))
	if len(mg.Responses) != 4 {
		t.Fatalf("multiget: %v", mg.hrefs())
	}
	if data, ok := mg.prop(0, "calendar-data"); !ok || data != simpleEvent("a", "Ana editada") {
		t.Fatalf("calendar-data: %q %v", data, ok)
	}
	for i := 1; i < 4; i++ {
		if !strings.Contains(mg.Responses[i].Status, "404") {
			t.Errorf("respuesta %d: un borrado, un href con .. y uno de libretas van en 404: %+v", i, mg.Responses[i])
		}
	}
	// Un token sobre otro calendario o roto obliga a sincronizar de nuevo.
	for _, token := range []string{"basura", "urn:mail-dav:sync:" + uuid.NewString() + ":3"} {
		rec := hn.req("REPORT", calPath, fmt.Sprintf(davx5CalSync, token))
		expectStatus(t, rec, http.StatusForbidden)
		if !strings.Contains(rec.Body.String(), "valid-sync-token") {
			t.Fatalf("cuerpo: %s", rec.Body.String())
		}
	}
	expectStatus(t, hn.req("REPORT", calPath, strings.Replace(davx5CalSync, "<d:sync-level>1", "<d:sync-level>infinite", 1)), http.StatusBadRequest)
	// Los cambios de las libretas no mueven el token del calendario ni al reves.
	hn.req("PROPFIND", homePath, "", "Depth", "1")
	hn.putCard("c.vcf", "c", "Carla")
	if again := sync(delta.SyncToken); len(again.Responses) != 0 {
		t.Fatalf("un contacto no es un cambio del calendario: %v", again.hrefs())
	}
}

func TestMkcalendarComoIOSYBorradoDeCalendarios(t *testing.T) {
	hn := newHarness(t)
	expectStatus(t, hn.req("MKCALENDAR", calHome+"trabajo/", iosMkcalendar), http.StatusCreated)
	ms := parseMulti(t, hn.req("PROPFIND", calHome+"trabajo/", `<propfind xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><prop><displayname/><C:calendar-description/></prop></propfind>`, "Depth", "0"))
	if name, _ := ms.prop(0, "displayname"); name != "Trabajo" {
		t.Fatalf("nombre: %q", name)
	}
	if d, _ := ms.prop(0, "calendar-description"); d != "Reuniones" {
		t.Fatalf("descripcion: %q", d)
	}
	expectStatus(t, hn.req("MKCALENDAR", calHome+"trabajo/", iosMkcalendar), http.StatusMethodNotAllowed)
	expectStatus(t, hn.req("MKCALENDAR", calHome+"sin-cuerpo", ""), http.StatusCreated)
	expectStatus(t, hn.req("MKCALENDAR", calHome+"tercero", ""), http.StatusInsufficientStorage)
	tasks := `<C:mkcalendar xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:"><D:set><D:prop><C:supported-calendar-component-set><C:comp name="VTODO"/></C:supported-calendar-component-set></D:prop></D:set></C:mkcalendar>`
	rec := hn.req("MKCALENDAR", calHome+"tareas", tasks)
	expectStatus(t, rec, http.StatusForbidden)
	if !strings.Contains(rec.Body.String(), "supported-calendar-component") {
		t.Fatalf("cuerpo: %s", rec.Body.String())
	}
	expectStatus(t, hn.req("MKCALENDAR", calHome+"Mayusculas", ""), http.StatusForbidden)
	expectStatus(t, hn.req("MKCALENDAR", calHome, ""), http.StatusForbidden)
	expectStatus(t, hn.req("MKCALENDAR", homePath+"libreta", ""), http.StatusForbidden)
	expectStatus(t, hn.req("MKCALENDAR", calHome+"x", `<D:mkcol xmlns:D="DAV:"/>`), http.StatusBadRequest)

	hn.req("PUT", calHome+"trabajo/x.ics", simpleEvent("x", "X"), "Content-Type", textCal)
	expectStatus(t, hn.req("DELETE", calHome+"trabajo/", ""), http.StatusNoContent)
	expectStatus(t, hn.req("GET", calHome+"trabajo/x.ics", ""), http.StatusNotFound)
	expectStatus(t, hn.req("DELETE", calHome+"trabajo/", ""), http.StatusNotFound)
}

func TestMkcolExtendidoCreaCalendariosYLibretasSoloDeSuTipo(t *testing.T) {
	hn := newHarness(t)
	calendar := `<D:mkcol xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:set><D:prop><D:resourcetype><D:collection/><C:calendar/></D:resourcetype><D:displayname>Extendido</D:displayname></D:prop></D:set></D:mkcol>`
	expectStatus(t, hn.req("MKCOL", calHome+"ext/", calendar), http.StatusCreated)
	expectStatus(t, hn.req("MKCOL", calHome+"simple", ""), http.StatusCreated)
	book := `<D:mkcol xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:set><D:prop><D:resourcetype><D:collection/><C:addressbook/></D:resourcetype></D:prop></D:set></D:mkcol>`
	expectStatus(t, hn.req("MKCOL", calHome+"libreta", book), http.StatusForbidden)
	expectStatus(t, hn.req("MKCOL", calHome, ""), http.StatusForbidden)
	ms := parseMulti(t, hn.req("PROPFIND", calHome+"ext/", `<propfind xmlns="DAV:"><prop><displayname/></prop></propfind>`, "Depth", "0"))
	if name, _ := ms.prop(0, "displayname"); name != "Extendido" {
		t.Fatalf("nombre: %q", name)
	}
}

func TestUnBuzonNoVeLosEventosDeOtro(t *testing.T) {
	hn := newHarness(t)
	hn.discover()
	hn.putEvent("secreto.ics", simpleEvent("s", "Secreto de Ana"))
	beaHome := base + "/calendars/bea@beta.test/"
	crisHome := base + "/calendars/cris@acme.test/"
	hn.as(bea, "PROPFIND", beaHome, "", "Depth", "1")
	hn.as(cris, "PROPFIND", crisHome, "", "Depth", "1")

	for name, acc := range map[string]apptest.Account{"otra empresa": bea, "misma empresa": cris} {
		for _, method := range []string{"GET", "HEAD", "DELETE", "PROPFIND"} {
			rec := hn.as(acc, method, calPath+"secreto.ics", "", "Depth", "0")
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s: %d", name, method, rec.Code)
			}
			if strings.Contains(rec.Body.String(), "Secreto") {
				t.Errorf("%s %s: filtra el evento", name, method)
			}
		}
		expectStatus(t, hn.as(acc, "PROPFIND", calHome, "", "Depth", "1"), http.StatusNotFound)
		expectStatus(t, hn.as(acc, "PUT", calPath+"nuevo.ics", simpleEvent("n", "N"), "Content-Type", textCal), http.StatusNotFound)
		expectStatus(t, hn.as(acc, "REPORT", calPath, fmt.Sprintf(calQueryWith, "")), http.StatusNotFound)
		expectStatus(t, hn.as(acc, "MKCALENDAR", calHome+"mio/", ""), http.StatusNotFound)
	}
	for _, acc := range []struct {
		acc  apptest.Account
		home string
	}{{bea, beaHome}, {cris, crisHome}} {
		mg := parseMulti(t, hn.as(acc.acc, "REPORT", acc.home+"calendar/", fmt.Sprintf(davx5CalMultiget, "<d:href>"+calPath+"secreto.ics</d:href>"), "Depth", "1"))
		if len(mg.Responses) != 1 || !strings.Contains(mg.Responses[0].Status, "404") {
			t.Errorf("multiget con un href ajeno: %+v", mg.Responses)
		}
		all := parseMulti(t, hn.as(acc.acc, "REPORT", acc.home+"calendar/", fmt.Sprintf(calQueryWith, "")))
		if len(all.Responses) != 0 {
			t.Errorf("el calendario de otro buzon no trae eventos de Ana: %v", all.hrefs())
		}
		rng := parseMulti(t, hn.as(acc.acc, "REPORT", acc.home+"calendar/", fmt.Sprintf(thunderbirdRange, "20260101T000000Z", "20271231T000000Z")))
		if len(rng.Responses) != 0 {
			t.Errorf("la consulta por rango de otro buzon no trae eventos de Ana: %v", rng.hrefs())
		}
	}
	expectStatus(t, hn.req("GET", calPath+"secreto.ics", ""), http.StatusOK)
}

func TestRutasDeCalendariosConTruco(t *testing.T) {
	hn := newHarness(t)
	hn.discover()
	hn.putEvent("a.ics", simpleEvent("a", "A"))
	for name, path := range map[string]string{
		"punto punto":            calHome + "calendar/../calendar/a.ics",
		"punto punto codificado": calHome + "calendar/%2e%2e/calendar/a.ics",
		"barra codificada":       calHome + "calendar%2Fa.ics",
		"barra invertida":        calHome + "calendar/..%5Ca.ics",
		"doble barra":            calHome + "calendar//a.ics",
		"NUL":                    calHome + "calendar/a.ics%00",
		"salto de linea":         calHome + "calendar/a%0a.ics",
		"prefijo pegado":         base + "x/calendars/ana@acme.test/calendar/a.ics",
		"demasiado profundo":     calPath + "a.ics/mas/y/mas",
		"usuario de otro":        base + "/calendars/bea@beta.test/calendar/a.ics",
		"usuario con barra":      base + "/calendars/ana@acme.test%2F..%2Fbea@beta.test/calendar/a.ics",
		"evento con barra final": calPath + "a.ics/",
	} {
		for _, method := range []string{"GET", "PROPFIND", "DELETE"} {
			rec := hn.req(method, path, "")
			if rec.Code < 400 || rec.Code == http.StatusMultiStatus {
				t.Errorf("%s %s: %d", name, method, rec.Code)
			}
			if strings.Contains(rec.Body.String(), "BEGIN:VCALENDAR") {
				t.Errorf("%s %s: devolvio datos", name, method)
			}
		}
	}
	expectStatus(t, hn.req("GET", calPath+"a.ics", ""), http.StatusOK)
	expectStatus(t, hn.req("GET", base+"/calendars/Ana@ACME.test/calendar/a.ics", ""), http.StatusOK)
	// Un evento no se lee por la ruta de las libretas ni un contacto por la de los calendarios.
	expectStatus(t, hn.req("GET", homePath+"contacts/a.ics", ""), http.StatusNotFound)
	hn.putCard("c.vcf", "c", "C")
	expectStatus(t, hn.req("GET", calPath+"c.vcf", ""), http.StatusNotFound)
}

func TestXMLHostilEnInformesDeCalendario(t *testing.T) {
	hn := newHarness(t)
	hn.discover()
	xxe := `<?xml version="1.0"?><!DOCTYPE foo [<!ENTITY xxe SYSTEM "file:///etc/passwd">]>` +
		`<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:"><D:prop><D:getetag/></D:prop><C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"><C:prop-filter name="SUMMARY"><C:text-match>&xxe;</C:text-match></C:prop-filter></C:comp-filter></C:comp-filter></C:filter></C:calendar-query>`
	rec := hn.req("REPORT", calPath, xxe)
	expectStatus(t, rec, http.StatusBadRequest)
	if strings.Contains(rec.Body.String(), "root:") {
		t.Fatal("una entidad externa se resolvio")
	}
	huge := `<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav">` + strings.Repeat("<x/>", 10000) + `</C:calendar-query>`
	expectStatus(t, hn.req("REPORT", calPath, huge), http.StatusRequestEntityTooLarge)
	deep := `<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav"><C:filter>` + strings.Repeat("<C:comp-filter name=\"VCALENDAR\">", 150) + strings.Repeat("</C:comp-filter>", 150) + `</C:filter></C:calendar-query>`
	if rec := hn.req("REPORT", calPath, deep); rec.Code != http.StatusForbidden && rec.Code != http.StatusBadRequest {
		t.Fatalf("anidamiento profundo: %d", rec.Code)
	}
	tooMany := `<C:calendar-multiget xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:"><D:prop><D:getetag/></D:prop>` + strings.Repeat("<D:href>/x.ics</D:href>", calendarLimits.MaxEventsPerMailbox+1) + `</C:calendar-multiget>`
	expectStatus(t, hn.req("REPORT", calPath, tooMany), http.StatusRequestEntityTooLarge)
	// Lo que el cliente escribe en un nombre de calendario o en un evento sale escapado.
	expectStatus(t, hn.req("MKCALENDAR", calHome+"xss", `<C:mkcalendar xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:"><D:set><D:prop><D:displayname>&lt;script&gt;alert(1)&lt;/script&gt;</D:displayname></D:prop></D:set></C:mkcalendar>`), http.StatusCreated)
	body := hn.req("PROPFIND", calHome+"xss/", "", "Depth", "0").Body.String()
	if strings.Contains(body, "<script>") || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("el nombre se escribio sin escapar: %s", body)
	}
	hn.putEvent("x.ics", ics("x", "SUMMARY:</C:calendar-data><evil/>", "DTSTART:20260921T100000Z"))
	all := hn.req("REPORT", calPath, `<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:"><D:prop><C:calendar-data/></D:prop><C:filter><C:comp-filter name="VCALENDAR"/></C:filter></C:calendar-query>`)
	if strings.Contains(all.Body.String(), "<evil/>") {
		t.Fatalf("el contenido del evento rompio el XML: %s", all.Body.String())
	}
	ms := parseMulti(t, all)
	if data, _ := ms.prop(0, "calendar-data"); !strings.Contains(data, "</C:calendar-data><evil/>") {
		t.Fatalf("el contenido se altero: %q", data)
	}
}

func TestLasContrasenasNoApareceEnLosRegistrosDeCalDAV(t *testing.T) {
	hn := newHarness(t)
	hn.discover()
	hn.putEvent("x.ics", "no es un iCalendar")
	hn.putEvent("y.ics", strings.Repeat("A", calendarLimits.MaxEventBytes+1))
	for _, e := range hn.logs.All() {
		if strings.Contains(fmt.Sprint(e.Message, e.ContextMap()), ana.Password) {
			t.Fatalf("un registro contiene la contrasena: %v", e)
		}
	}
}

func TestLosEventosDeRecurrenciaHostilNoBloqueanLaConsulta(t *testing.T) {
	hn := newHarness(t)
	hn.discover()
	hostile := ics("hostil", "SUMMARY:Hostil", "DTSTART:20260101T100000Z", "DTEND:20260101T110000Z", "RRULE:FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=30")
	expectStatus(t, hn.putEvent("hostil.ics", hostile), http.StatusCreated)
	hn.putEvent("normal.ics", simpleEvent("normal", "Normal"))
	// La regla que nunca coincide agota su presupuesto y el evento se devuelve (sin poder descartarlo);
	// el otro se decide con exactitud y queda fuera del rango.
	ms := parseMulti(t, hn.req("REPORT", calPath, fmt.Sprintf(thunderbirdRange, "20301201T000000Z", "20301202T000000Z")))
	if got := ms.hrefs(); len(got) != 1 || got[0] != calPath+"hostil.ics" {
		t.Fatalf("resultado: %v", got)
	}
}
