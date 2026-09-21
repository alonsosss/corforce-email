package http

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"strings"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
)

const (
	maxPropFilters    = 32
	maxTextMatches    = 16
	maxTextMatchBytes = 1024
)

var (
	nameMultiget = cardName("addressbook-multiget")
	nameQuery    = cardName("addressbook-query")
	nameSync     = davName("sync-collection")
)

// report atiende los tres informes de RFC 6352 y RFC 6578 que usan los clientes, siempre sobre una
// libreta del propio buzon.
func (h *Handler) report(w http.ResponseWriter, r *http.Request, p domain.Principal, t target) {
	if t.kind == kindCalendar {
		h.calendarReport(w, r, p, t)
		return
	}
	if t.kind != kindBook {
		writeDAVError(w, http.StatusForbidden, davName("supported-report"))
		return
	}
	var (
		multiget multigetReq
		query    queryReq
		sync     syncReq
	)
	root, err := decodeRoot(http.MaxBytesReader(w, r.Body, h.cfg.MaxXMLBytes), func(n xml.Name) any {
		switch n {
		case nameMultiget:
			return &multiget
		case nameQuery:
			return &query
		case nameSync:
			return &sync
		}
		return nil
	})
	if err != nil {
		badBody(w, err)
		return
	}
	switch root {
	case nameMultiget:
		h.multiget(w, r, p, t, multiget)
	case nameQuery:
		h.query(w, r, p, t, query)
	case nameSync:
		h.sync(w, r, p, t, sync)
	default:
		writeDAVError(w, http.StatusForbidden, davName("supported-report"))
	}
}

func (h *Handler) writeReport(w http.ResponseWriter, r *http.Request, ms multistatus) {
	if err := writeXML(w, http.StatusMultiStatus, ms); err != nil {
		h.fail(w, r, err)
	}
}

func (h *Handler) multiget(w http.ResponseWriter, r *http.Request, p domain.Principal, t target, req multigetReq) {
	if len(req.Hrefs) > h.uc.Limits().MaxContactsPerMailbox {
		http.Error(w, "demasiados href en la peticion", http.StatusRequestEntityTooLarge)
		return
	}
	if req.Prop.tooMany() {
		tooManyProps(w)
		return
	}
	ctx := r.Context()
	book, err := h.uc.Addressbook(ctx, p, t.slug)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	hrefs := uniqueHrefs(req.Hrefs, func(href string) (string, bool) { return h.contactNameOf(href, p, t.slug) })
	names := make([]string, 0, len(hrefs))
	byHref := make(map[string]string, len(hrefs))
	for _, href := range hrefs {
		if name, ok := h.contactNameOf(href, p, t.slug); ok {
			names = append(names, name)
			byHref[href] = name
		}
	}
	props := propRequestFrom(req.Prop)
	contacts, err := h.uc.ContactsByName(ctx, p, t.slug, names, props.wantsData())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	found := make(map[string]domain.Contact, len(contacts))
	for _, c := range contacts {
		found[c.ResourceName] = c
	}
	ms := multistatus{Responses: make([]response, 0, len(hrefs))}
	for _, href := range hrefs {
		c, ok := found[byHref[href]]
		if !ok {
			ms.Responses = append(ms.Responses, response{Href: hrefText(hrefPath(href)), Status: statusLine(http.StatusNotFound)})
			continue
		}
		ms.Responses = append(ms.Responses, h.respond(p, h.contactResource(p, book, c), props))
	}
	h.writeReport(w, r, ms)
}

// uniqueHrefs quita de un multiget los href repetidos, y los que nombran el mismo recurso con otra
// escritura (%61.vcf y a.vcf): cada recurso se responde una vez, porque repetir un href multiplicaria el
// contenido de la respuesta por lo que quepa en el cuerpo de la peticion.
func uniqueHrefs(hrefs []string, nameOf func(string) (string, bool)) []string {
	seen := make(map[string]bool, len(hrefs))
	out := make([]string, 0, len(hrefs))
	for _, href := range hrefs {
		key := "h:" + href
		if name, ok := nameOf(href); ok {
			key = "n:" + name
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, href)
		}
	}
	return out
}

// contactNameOf devuelve el nombre del contacto al que apunta un href, si es de esta libreta del propio
// buzon. Un href a otra libreta, a otro buzon o mal formado se responde como inexistente.
func (h *Handler) contactNameOf(href string, p domain.Principal, slug string) (string, bool) {
	tg, ok := h.routePath(hrefPath(href), p)
	if !ok || tg.kind != kindContact || tg.slug != slug {
		return "", false
	}
	return tg.resource, true
}

// hrefPath acepta un href como ruta o como URL absoluta y devuelve su ruta con escapes.
func hrefPath(href string) string {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return ""
	}
	return u.EscapedPath()
}

func (h *Handler) query(w http.ResponseWriter, r *http.Request, p domain.Principal, t target, req queryReq) {
	filter, cond, ok := filterFrom(req.Filter)
	if !ok {
		writeDAVError(w, http.StatusForbidden, cond)
		return
	}
	limit := 0
	if req.Limit != nil && req.Limit.NResults > 0 {
		limit = req.Limit.NResults
	}
	if req.Prop.tooMany() {
		tooManyProps(w)
		return
	}
	book, contacts, err := h.uc.Query(r.Context(), p, t.slug, filter, limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	props := propRequestFrom(req.Prop)
	ms := multistatus{Responses: make([]response, 0, len(contacts))}
	for _, c := range contacts {
		ms.Responses = append(ms.Responses, h.respond(p, h.contactResource(p, book, c), props))
	}
	h.writeReport(w, r, ms)
}

func (h *Handler) sync(w http.ResponseWriter, r *http.Request, p domain.Principal, t target, req syncReq) {
	if level := strings.TrimSpace(req.Level); level != "" && level != "1" {
		http.Error(w, "solo se admite sync-level 1", http.StatusBadRequest)
		return
	}
	if req.Prop.tooMany() {
		tooManyProps(w)
		return
	}
	ctx := r.Context()
	props := propRequestFrom(req.Prop)
	result, err := h.uc.Sync(ctx, p, t.slug, req.Token, props.wantsData())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	book, err := h.uc.Addressbook(ctx, p, t.slug)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ms := multistatus{Responses: make([]response, 0, len(result.Changed)+len(result.Removed)), SyncToken: result.Token}
	for _, c := range result.Changed {
		ms.Responses = append(ms.Responses, h.respond(p, h.contactResource(p, book, c), props))
	}
	for _, name := range result.Removed {
		ms.Responses = append(ms.Responses, response{Href: hrefText(h.contactPath(p, t.slug, name)), Status: statusLine(http.StatusNotFound)})
	}
	h.writeReport(w, r, ms)
}

// filterFrom convierte el filtro de addressbook-query. Lo que el servidor no sabe evaluar se rechaza con
// la precondicion que corresponde (supported-filter o supported-collation) en vez de ignorarlo, que
// devolveria contactos que el cliente excluyo.
func filterFrom(req *filterReq) (domain.Filter, xml.Name, bool) {
	if req == nil {
		return domain.Filter{}, xml.Name{}, true
	}
	if len(req.PropFilters) > maxPropFilters {
		return domain.Filter{}, cardName("supported-filter"), false
	}
	out := domain.Filter{AllOf: strings.EqualFold(req.Test, "allof")}
	for _, pf := range req.PropFilters {
		if len(pf.ParamFilters) > 0 {
			return domain.Filter{}, cardName("supported-filter"), false
		}
		f, cond, ok := propFilterFrom(pf.Name, pf.Test, pf.IsNotDefined != nil, pf.TextMatches, cardName)
		if !ok {
			return domain.Filter{}, cond, false
		}
		out.Props = append(out.Props, f)
	}
	return out, xml.Name{}, true
}

// propFilterFrom convierte un prop-filter de CardDAV o de CalDAV. Lo que no sabe evaluar (una colacion o un
// tipo de comparacion desconocidos, demasiadas comparaciones) se rechaza con la precondicion del espacio de
// nombres de quien lo pide (ns).
func propFilterFrom(name, test string, isNotDefined bool, matches []textMatchReq, ns func(string) xml.Name) (domain.PropFilter, xml.Name, bool) {
	name = strings.ToUpper(strings.TrimSpace(name))
	if name == "" || len(matches) > maxTextMatches {
		return domain.PropFilter{}, ns("supported-filter"), false
	}
	f := domain.PropFilter{Name: name, AllOf: strings.EqualFold(test, "allof"), IsNotDefined: isNotDefined}
	for _, tm := range matches {
		m := domain.TextMatch{Text: tm.Text, Collation: domain.CollationUnicodeCasemap, Type: domain.MatchContains, Negate: strings.EqualFold(tm.Negate, "yes")}
		if tm.Collation != "" {
			m.Collation = domain.Collation(tm.Collation)
		}
		if tm.MatchType != "" {
			m.Type = domain.MatchType(tm.MatchType)
		}
		switch {
		case !m.Collation.Valid():
			return domain.PropFilter{}, ns("supported-collation"), false
		case !m.Type.Valid() || len(m.Text) > maxTextMatchBytes:
			return domain.PropFilter{}, ns("supported-filter"), false
		}
		f.Matches = append(f.Matches, m)
	}
	return f, xml.Name{}, true
}
