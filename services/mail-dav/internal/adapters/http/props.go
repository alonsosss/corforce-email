package http

import (
	"encoding/xml"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
)

// resource es un recurso ya leido, con la ruta (sin escapar) que se anuncia en las respuestas.
type resource struct {
	kind    kind
	path    string
	book    *domain.Addressbook
	contact *domain.Contact
	// calendar y event son los de un recurso de CalDAV; event lleva tambien su calendario.
	calendar *domain.Calendar
	event    *domain.Event
}

type propMode int

const (
	modeAll propMode = iota
	modeNames
	modeListed
)

type propRequest struct {
	mode  propMode
	names []xml.Name
}

var (
	nameAddressData = cardName("address-data")
	nameGetETag     = davName("getetag")
)

// isDataProp dice si la propiedad es el contenido del objeto: solo se devuelve si se pide por su nombre.
func isDataProp(n xml.Name) bool { return n == nameAddressData || n == nameCalendarData }

// wantsData dice si la peticion pide el contenido de los objetos. Sin pedirlo, un listado no necesita leerlo.
func (r propRequest) wantsData() bool {
	if r.mode != modeListed {
		return false
	}
	for _, n := range r.names {
		if isDataProp(n) {
			return true
		}
	}
	return false
}

// reportProps son las propiedades de un REPORT que no pide ninguna: el etag y el vCard.
var reportProps = propRequest{mode: modeListed, names: []xml.Name{nameGetETag, nameAddressData}}

const vcardContentType = "text/vcard; charset=utf-8"

func etagHeader(etag string) string { return `"` + etag + `"` }

func (h *Handler) principalProps(p domain.Principal) []element {
	return []element{el(davName("current-user-principal"), "", hrefEl(h.principalPath(p)))}
}

// props es cada propiedad que el recurso conoce, en el orden en que se responden. address-data solo
// existe en un contacto y solo se devuelve si se pide por su nombre.
func (h *Handler) props(p domain.Principal, res resource) []element {
	out := h.principalProps(p)
	collection := el(davName("collection"), "")
	switch res.kind {
	case kindRoot, kindPrincipals, kindHomes, kindCalHomes:
		out = append(out, el(davName("resourcetype"), "", collection))
	case kindPrincipal:
		out = append(out,
			el(davName("resourcetype"), "", collection, el(davName("principal"), "")),
			el(davName("displayname"), p.Username),
			el(davName("principal-URL"), "", hrefEl(h.principalPath(p))),
			el(cardName("addressbook-home-set"), "", hrefEl(h.homePath(p))),
			el(calName("calendar-home-set"), "", hrefEl(h.calendarHomePath(p))))
	case kindHome, kindCalHome:
		out = append(out,
			el(davName("resourcetype"), "", collection),
			el(davName("displayname"), p.Username),
			el(davName("owner"), "", hrefEl(h.principalPath(p))),
			privileges("read", "write", "bind", "unbind"))
	case kindBook:
		out = append(out, h.bookProps(p, *res.book, collection)...)
	case kindContact:
		out = append(out, contactProps(*res.contact)...)
	case kindCalendar:
		out = append(out, h.calendarProps(p, *res.calendar, collection)...)
	case kindEvent:
		out = append(out, eventProps(*res.event)...)
	}
	return out
}

func privileges(names ...string) element {
	set := el(davName("current-user-privilege-set"), "")
	for _, n := range names {
		set.Children = append(set.Children, el(davName("privilege"), "", el(davName(n), "")))
	}
	return set
}

// supportedReports arma la propiedad supported-report-set con los informes que admite la coleccion.
func supportedReports(names ...xml.Name) element {
	set := el(davName("supported-report-set"), "")
	for _, n := range names {
		set.Children = append(set.Children, el(davName("supported-report"), "", el(davName("report"), "", el(n, ""))))
	}
	return set
}

func (h *Handler) bookProps(p domain.Principal, b domain.Addressbook, collection element) []element {
	dataType := func(version string) element {
		e := el(cardName("address-data-type"), "")
		e.Attrs = []xml.Attr{{Name: xml.Name{Local: "content-type"}, Value: "text/vcard"}, {Name: xml.Name{Local: "version"}, Value: version}}
		return e
	}
	return []element{
		el(davName("resourcetype"), "", collection, el(cardName("addressbook"), "")),
		el(davName("displayname"), b.DisplayName),
		el(cardName("addressbook-description"), b.Description),
		el(xml.Name{Space: nsCS, Local: "getctag"}, syncID(b)),
		el(davName("sync-token"), domain.SyncToken(b.ID, b.SyncSeq)),
		el(davName("getlastmodified"), b.UpdatedAt.UTC().Format(http.TimeFormat)),
		el(davName("owner"), "", hrefEl(h.principalPath(p))),
		privileges("read", "write", "write-content", "bind", "unbind"),
		supportedReports(cardName("addressbook-query"), cardName("addressbook-multiget"), davName("sync-collection")),
		el(cardName("supported-address-data"), "", dataType("3.0"), dataType("4.0")),
	}
}

func syncID(b domain.Addressbook) string {
	return b.ID.String() + ":" + strconv.FormatInt(b.SyncSeq, 10)
}

func contactProps(c domain.Contact) []element {
	return []element{
		el(davName("resourcetype"), ""),
		el(davName("getetag"), etagHeader(c.ETag)),
		el(davName("getcontenttype"), vcardContentType),
		el(davName("getcontentlength"), strconv.Itoa(c.Size)),
		el(davName("getlastmodified"), c.UpdatedAt.UTC().Format(http.TimeFormat)),
		el(davName("displayname"), c.DisplayName),
		privileges("read", "write-content", "unbind"),
		el(nameAddressData, c.VCard),
	}
}

func (h *Handler) contactResource(p domain.Principal, b domain.Addressbook, c domain.Contact) resource {
	book, contact := b, c
	return resource{kind: kindContact, path: h.contactPath(p, b.Slug, c.ResourceName), book: &book, contact: &contact}
}

func (h *Handler) bookResource(p domain.Principal, b domain.Addressbook) resource {
	book := b
	return resource{kind: kindBook, path: h.bookPath(p, b.Slug), book: &book}
}

// respond arma la respuesta de un recurso para las propiedades pedidas: las conocidas con su valor y,
// aparte, las que no conoce (404), como exige RFC 4918 9.1.
func (h *Handler) respond(p domain.Principal, res resource, req propRequest) response {
	all := h.props(p, res)
	found, missing := []element{}, []element{}
	switch req.mode {
	case modeAll:
		for _, e := range all {
			if !isDataProp(e.XMLName) {
				found = append(found, e)
			}
		}
	case modeNames:
		for _, e := range all {
			if !isDataProp(e.XMLName) {
				found = append(found, element{XMLName: e.XMLName})
			}
		}
	default:
		for _, want := range req.names {
			if e, ok := lookup(all, want); ok {
				found = append(found, e)
			} else {
				missing = append(missing, element{XMLName: want})
			}
		}
	}
	resp := response{Href: hrefText(res.path)}
	if len(found) > 0 || len(missing) == 0 {
		resp.Propstats = append(resp.Propstats, propstat{Prop: propBody{Items: found}, Status: statusLine(http.StatusOK)})
	}
	if len(missing) > 0 {
		resp.Propstats = append(resp.Propstats, propstat{Prop: propBody{Items: missing}, Status: statusLine(http.StatusNotFound)})
	}
	return resp
}

func lookup(list []element, name xml.Name) (element, bool) {
	for _, e := range list {
		if e.XMLName == name {
			return e, true
		}
	}
	return element{}, false
}

func propRequestFrom(list *propList) propRequest { return propRequestOr(list, reportProps) }

// propRequestOr es el prop de un informe; sin ninguno, las propiedades por omision del tipo de coleccion.
func propRequestOr(list *propList, fallback propRequest) propRequest {
	if list == nil || len(list.Items) == 0 {
		return fallback
	}
	return propRequest{mode: modeListed, names: list.names()}
}

func (h *Handler) requestedProps(r *http.Request, w http.ResponseWriter) (propRequest, bool) {
	var req propfindReq
	err := decodeDoc(http.MaxBytesReader(w, r.Body, h.cfg.MaxXMLBytes), &req)
	switch {
	case errors.Is(err, errEmptyBody):
		return propRequest{mode: modeAll}, true
	case err != nil:
		badBody(w, err)
		return propRequest{}, false
	case req.Prop.tooMany():
		tooManyProps(w)
		return propRequest{}, false
	case req.PropName != nil:
		return propRequest{mode: modeNames}, true
	case req.Prop != nil:
		return propRequest{mode: modeListed, names: req.Prop.names()}, true
	}
	return propRequest{mode: modeAll}, true
}

// tooManyProps responde al prop con mas propiedades de las que un cliente pide: cada una se repite en la
// respuesta de cada recurso, y el cuerpo permite decenas de miles.
func tooManyProps(w http.ResponseWriter) {
	http.Error(w, "demasiadas propiedades pedidas", http.StatusBadRequest)
}

// badBody distingue un cuerpo demasiado grande de uno mal formado.
func badBody(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		http.Error(w, "el cuerpo supera el tamano maximo", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "cuerpo XML no valido", http.StatusBadRequest)
}

func (h *Handler) propfind(w http.ResponseWriter, r *http.Request, p domain.Principal, t target) {
	depth := strings.ToLower(strings.TrimSpace(r.Header.Get("Depth")))
	switch depth {
	case "", "1":
		depth = "1"
	case "0":
	default:
		writeDAVError(w, http.StatusForbidden, davName("propfind-finite-depth"))
		return
	}
	req, ok := h.requestedProps(r, w)
	if !ok {
		return
	}
	ctx := r.Context()
	var out []resource
	switch t.kind {
	case kindRoot:
		out = []resource{{kind: kindRoot, path: h.cfg.BasePath + "/"}}
	case kindPrincipals:
		out = []resource{{kind: kindPrincipals, path: h.path("principals") + "/"}}
	case kindPrincipal:
		out = []resource{{kind: kindPrincipal, path: h.principalPath(p)}}
	case kindHomes:
		out = []resource{{kind: kindHomes, path: h.path("addressbooks") + "/"}}
	case kindHome:
		out = []resource{{kind: kindHome, path: h.homePath(p)}}
		if depth == "1" {
			books, err := h.uc.Addressbooks(ctx, p)
			if err != nil {
				h.fail(w, r, err)
				return
			}
			for _, b := range books {
				out = append(out, h.bookResource(p, b))
			}
		}
	case kindBook:
		if depth == "0" {
			b, err := h.uc.Addressbook(ctx, p, t.slug)
			if err != nil {
				h.fail(w, r, err)
				return
			}
			out = []resource{h.bookResource(p, b)}
			break
		}
		b, contacts, err := h.uc.Contacts(ctx, p, t.slug, req.wantsData())
		if err != nil {
			h.fail(w, r, err)
			return
		}
		out = append(out, h.bookResource(p, b))
		for _, c := range contacts {
			out = append(out, h.contactResource(p, b, c))
		}
	case kindCalHomes, kindCalHome, kindCalendar, kindEvent:
		var err error
		if out, err = h.calendarResources(ctx, p, t, depth, req.wantsData()); err != nil {
			h.fail(w, r, err)
			return
		}
	case kindContact:
		b, err := h.uc.Addressbook(ctx, p, t.slug)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		c, err := h.uc.Contact(ctx, p, t.slug, t.resource)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		out = []resource{h.contactResource(p, b, c)}
	}
	ms := multistatus{Responses: make([]response, 0, len(out))}
	for _, res := range out {
		ms.Responses = append(ms.Responses, h.respond(p, res, req))
	}
	if err := writeXML(w, http.StatusMultiStatus, ms); err != nil {
		h.fail(w, r, err)
	}
}
