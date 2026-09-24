package http

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
)

var (
	nameCalendarData = calName("calendar-data")
	nameCalMultiget  = calName("calendar-multiget")
	nameCalQuery     = calName("calendar-query")
)

const (
	icalContentType    = "text/calendar; charset=utf-8"
	maxCompFilters     = 8
	componentVEvent    = "VEVENT"
	icalMediaTypeValue = "text/calendar"
)

// reportCalendarProps son las propiedades de un informe de calendario que no pide ninguna: el etag y el
// objeto de calendario.
var reportCalendarProps = propRequest{mode: modeListed, names: []xml.Name{nameGetETag, nameCalendarData}}

func (h *Handler) calendarHomePath(p domain.Principal) string {
	return h.path("calendars", p.Username) + "/"
}

func (h *Handler) calendarPath(p domain.Principal, slug string) string {
	return h.path("calendars", p.Username, slug) + "/"
}

func (h *Handler) eventPath(p domain.Principal, slug, resource string) string {
	return h.path("calendars", p.Username, slug, resource)
}

func (h *Handler) calendarResource(p domain.Principal, c domain.Calendar) resource {
	cal := c
	return resource{kind: kindCalendar, path: h.calendarPath(p, c.Slug), calendar: &cal}
}

func (h *Handler) eventResource(p domain.Principal, c domain.Calendar, e domain.Event) resource {
	cal, ev := c, e
	return resource{kind: kindEvent, path: h.eventPath(p, c.Slug, e.ResourceName), calendar: &cal, event: &ev}
}

func (h *Handler) calendarProps(p domain.Principal, c domain.Calendar, collection element) []element {
	comp := el(calName("comp"), "")
	comp.Attrs = []xml.Attr{{Name: xml.Name{Local: "name"}, Value: componentVEvent}}
	data := el(calName("calendar-data"), "")
	data.Attrs = []xml.Attr{{Name: xml.Name{Local: "content-type"}, Value: icalMediaTypeValue}, {Name: xml.Name{Local: "version"}, Value: "2.0"}}
	return []element{
		el(davName("resourcetype"), "", collection, el(calName("calendar"), "")),
		el(davName("displayname"), c.DisplayName),
		el(calName("calendar-description"), c.Description),
		el(xml.Name{Space: nsCS, Local: "getctag"}, syncID(c)),
		el(davName("sync-token"), domain.SyncToken(c.ID, c.SyncSeq)),
		el(davName("getlastmodified"), c.UpdatedAt.UTC().Format(http.TimeFormat)),
		el(davName("owner"), "", hrefEl(h.principalPath(p))),
		privileges("read", "write", "write-content", "bind", "unbind"),
		supportedReports(calName("calendar-query"), calName("calendar-multiget"), davName("sync-collection")),
		el(calName("supported-calendar-component-set"), "", comp),
		el(calName("supported-calendar-data"), "", data),
		el(calName("max-resource-size"), strconv.Itoa(h.uc.CalendarLimits().MaxEventBytes)),
	}
}

func eventProps(e domain.Event) []element {
	return []element{
		el(davName("resourcetype"), ""),
		el(davName("getetag"), etagHeader(e.ETag)),
		el(davName("getcontenttype"), icalContentType+"; component=vevent"),
		el(davName("getcontentlength"), strconv.Itoa(e.Size)),
		el(davName("getlastmodified"), e.UpdatedAt.UTC().Format(http.TimeFormat)),
		privileges("read", "write-content", "unbind"),
		el(nameCalendarData, e.ICal),
	}
}

// calendarResources arma los recursos que devuelve un PROPFIND sobre las rutas de calendarios.
func (h *Handler) calendarResources(ctx context.Context, p domain.Principal, t target, depth string, withData bool) ([]resource, error) {
	switch t.kind {
	case kindCalHomes:
		return []resource{{kind: kindCalHomes, path: h.path("calendars") + "/"}}, nil
	case kindCalHome:
		out := []resource{{kind: kindCalHome, path: h.calendarHomePath(p)}}
		if depth == "1" {
			cals, err := h.uc.Calendars(ctx, p)
			if err != nil {
				return nil, err
			}
			for _, c := range cals {
				out = append(out, h.calendarResource(p, c))
			}
		}
		return out, nil
	case kindCalendar:
		if depth == "0" {
			c, err := h.uc.Calendar(ctx, p, t.slug)
			if err != nil {
				return nil, err
			}
			return []resource{h.calendarResource(p, c)}, nil
		}
		c, events, err := h.uc.Events(ctx, p, t.slug, withData)
		if err != nil {
			return nil, err
		}
		out := []resource{h.calendarResource(p, c)}
		for _, e := range events {
			out = append(out, h.eventResource(p, c, e))
		}
		return out, nil
	default:
		c, err := h.uc.Calendar(ctx, p, t.slug)
		if err != nil {
			return nil, err
		}
		e, err := h.uc.Event(ctx, p, t.slug, t.resource)
		if err != nil {
			return nil, err
		}
		return []resource{h.eventResource(p, c, e)}, nil
	}
}

// icalMediaType admite el tipo con el que los clientes envian un iCalendar, y solo UTF-8.
func icalMediaType(header string) bool {
	if strings.TrimSpace(header) == "" {
		return true
	}
	media, params, err := mime.ParseMediaType(header)
	if err != nil || !strings.EqualFold(media, icalMediaTypeValue) {
		return false
	}
	cs, ok := params["charset"]
	return !ok || strings.EqualFold(cs, "utf-8")
}

func (h *Handler) putEvent(w http.ResponseWriter, r *http.Request, p domain.Principal, t target) {
	if !icalMediaType(r.Header.Get("Content-Type")) {
		writeDAVError(w, http.StatusUnsupportedMediaType, calName("supported-calendar-data"))
		return
	}
	limit := int64(h.uc.CalendarLimits().MaxEventBytes)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeDAVError(w, http.StatusForbidden, calName("max-resource-size"))
			return
		}
		http.Error(w, "no se pudo leer el cuerpo", http.StatusBadRequest)
		return
	}
	etag, created, err := h.uc.PutEvent(r.Context(), p, t.slug, t.resource, string(body), preconditionFrom(r.Header))
	var conflict *domain.UIDConflictError
	if errors.As(err, &conflict) {
		var children []element
		if conflict.Resource != "" {
			children = append(children, hrefEl(h.eventPath(p, t.slug, conflict.Resource)))
		}
		writeDAVError(w, http.StatusConflict, calName("no-uid-conflict"), children...)
		return
	}
	if errors.Is(err, domain.ErrNotFound) {
		http.Error(w, "el calendario no existe", http.StatusConflict)
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etagHeader(etag))
	if created {
		w.WriteHeader(http.StatusCreated)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// mkcalendar crea un calendario (MKCALENDAR de RFC 4791, o MKCOL extendido con el tipo calendar). Solo se
// admite el componente VEVENT; el resto de propiedades que fija el cliente y el servidor no guarda (color,
// orden) se ignoran.
func (h *Handler) mkcalendar(w http.ResponseWriter, r *http.Request, p domain.Principal, t target) {
	if t.kind != kindCalendar {
		http.Error(w, "solo se pueden crear calendarios", http.StatusForbidden)
		return
	}
	var req mkcalendarReq
	err := decodeDoc(http.MaxBytesReader(w, r.Body, h.cfg.MaxXMLBytes), &req)
	if err != nil && !errors.Is(err, errEmptyBody) {
		badBody(w, err)
		return
	}
	h.createCalendar(w, r, p, t, req.Set, false)
}

// createCalendar aplica las propiedades fijadas y crea el calendario. typed exige que el MKCOL declare el
// tipo calendar (un MKCOL extendido de otro tipo no crea un calendario).
func (h *Handler) createCalendar(w http.ResponseWriter, r *http.Request, p domain.Principal, t target, sets []setBlock, typed bool) {
	var displayName, description string
	for _, set := range sets {
		if rt := set.Prop.ResourceType; typed && rt != nil && !hasName(rt.Items, calName("calendar")) {
			writeDAVError(w, http.StatusForbidden, davName("valid-resourcetype"))
			return
		}
		if set.Prop.Components != nil {
			for _, comp := range set.Prop.Components.Comps {
				if !strings.EqualFold(comp.Name, componentVEvent) {
					writeDAVError(w, http.StatusForbidden, calName("supported-calendar-component"))
					return
				}
			}
		}
		if set.Prop.DisplayName != nil {
			displayName = *set.Prop.DisplayName
		}
		if set.Prop.CalendarDescription != nil {
			description = *set.Prop.CalendarDescription
		}
	}
	if _, err := h.uc.CreateCalendar(r.Context(), p, t.slug, displayName, description); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// calendarReport atiende los informes de RFC 4791 y RFC 6578 sobre un calendario del propio buzon.
func (h *Handler) calendarReport(w http.ResponseWriter, r *http.Request, p domain.Principal, t target) {
	var (
		multiget calMultigetReq
		query    calQueryReq
		sync     syncReq
	)
	root, err := decodeRoot(http.MaxBytesReader(w, r.Body, h.cfg.MaxXMLBytes), func(n xml.Name) any {
		switch n {
		case nameCalMultiget:
			return &multiget
		case nameCalQuery:
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
	case nameCalMultiget:
		h.calendarMultiget(w, r, p, t, multiget)
	case nameCalQuery:
		h.calendarQuery(w, r, p, t, query)
	case nameSync:
		h.calendarSync(w, r, p, t, sync)
	default:
		writeDAVError(w, http.StatusForbidden, davName("supported-report"))
	}
}

// calendarPropRequest lee el prop de un informe. Un calendar-data con expand o limit-recurrence-set pide
// que el servidor expanda las recurrencias, que aqui no se hace: se rechaza en vez de devolver el objeto
// sin expandir a un cliente que espera instancias.
func calendarPropRequest(w http.ResponseWriter, list *calPropList) (propRequest, bool) {
	if list.tooMany() {
		tooManyProps(w)
		return propRequest{}, false
	}
	names, unsupported := list.names()
	if unsupported {
		writeDAVError(w, http.StatusForbidden, calName("supported-calendar-data"))
		return propRequest{}, false
	}
	return propRequestOr(names, reportCalendarProps), true
}

func (h *Handler) calendarMultiget(w http.ResponseWriter, r *http.Request, p domain.Principal, t target, req calMultigetReq) {
	if len(req.Hrefs) > h.uc.CalendarLimits().MaxEventsPerMailbox {
		http.Error(w, "demasiados href en la peticion", http.StatusRequestEntityTooLarge)
		return
	}
	props, ok := calendarPropRequest(w, req.Prop)
	if !ok {
		return
	}
	ctx := r.Context()
	cal, err := h.uc.Calendar(ctx, p, t.slug)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	hrefs := uniqueHrefs(req.Hrefs, func(href string) (string, bool) { return h.eventNameOf(href, p, t.slug) })
	names := make([]string, 0, len(hrefs))
	byHref := make(map[string]string, len(hrefs))
	for _, href := range hrefs {
		if name, ok := h.eventNameOf(href, p, t.slug); ok {
			names = append(names, name)
			byHref[href] = name
		}
	}
	events, err := h.uc.EventsByName(ctx, p, t.slug, names, props.wantsData())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	found := make(map[string]domain.Event, len(events))
	for _, e := range events {
		found[e.ResourceName] = e
	}
	ms := multistatus{Responses: make([]response, 0, len(hrefs))}
	for _, href := range hrefs {
		e, ok := found[byHref[href]]
		if !ok {
			ms.Responses = append(ms.Responses, response{Href: hrefText(hrefPath(href)), Status: statusLine(http.StatusNotFound)})
			continue
		}
		ms.Responses = append(ms.Responses, h.respond(p, h.eventResource(p, cal, e), props))
	}
	h.writeReport(w, r, ms)
}

// eventNameOf devuelve el nombre del evento al que apunta un href, si es de este calendario del propio
// buzon. Un href a otro calendario, a otro buzon o mal formado se responde como inexistente.
func (h *Handler) eventNameOf(href string, p domain.Principal, slug string) (string, bool) {
	tg, ok := h.routePath(hrefPath(href), p)
	if !ok || tg.kind != kindEvent || tg.slug != slug {
		return "", false
	}
	return tg.resource, true
}

func (h *Handler) calendarQuery(w http.ResponseWriter, r *http.Request, p domain.Principal, t target, req calQueryReq) {
	if len(req.Others) > 0 && !onlyTimezone(req.Others) {
		http.Error(w, "elemento no reconocido en calendar-query", http.StatusBadRequest)
		return
	}
	filter, fail := calendarFilterFrom(req.Filter)
	if fail != nil {
		if fail.condition.Local == "" {
			http.Error(w, fail.reason, fail.status)
			return
		}
		writeDAVError(w, fail.status, fail.condition)
		return
	}
	props, ok := calendarPropRequest(w, req.Prop)
	if !ok {
		return
	}
	cal, events, err := h.uc.QueryEvents(r.Context(), p, t.slug, filter)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ms := multistatus{Responses: make([]response, 0, len(events))}
	for _, e := range events {
		ms.Responses = append(ms.Responses, h.respond(p, h.eventResource(p, cal, e), props))
	}
	h.writeReport(w, r, ms)
}

// onlyTimezone dice si lo desconocido de un calendar-query se reduce a la zona con la que interpretar las
// horas flotantes, que el servidor no aplica: las toma como UTC.
func onlyTimezone(others []xmlName) bool {
	for _, o := range others {
		if o.XMLName != calName("timezone") && o.XMLName != calName("timezone-id") {
			return false
		}
	}
	return true
}

func (h *Handler) calendarSync(w http.ResponseWriter, r *http.Request, p domain.Principal, t target, req syncReq) {
	if level := strings.TrimSpace(req.Level); level != "" && level != "1" {
		http.Error(w, "solo se admite sync-level 1", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if req.Prop.tooMany() {
		tooManyProps(w)
		return
	}
	props := propRequestOr(req.Prop, reportCalendarProps)
	result, err := h.uc.SyncCalendar(ctx, p, t.slug, req.Token, props.wantsData())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	cal, err := h.uc.Calendar(ctx, p, t.slug)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ms := multistatus{Responses: make([]response, 0, len(result.Changed)+len(result.Removed)), SyncToken: result.Token}
	for _, e := range result.Changed {
		ms.Responses = append(ms.Responses, h.respond(p, h.eventResource(p, cal, e), props))
	}
	for _, name := range result.Removed {
		ms.Responses = append(ms.Responses, response{Href: hrefText(h.eventPath(p, t.slug, name)), Status: statusLine(http.StatusNotFound)})
	}
	h.writeReport(w, r, ms)
}

// filterFailure es un filtro que no se puede atender: con condicion es un 403 de precondicion de CalDAV, sin
// ella un cuerpo mal formado.
type filterFailure struct {
	status    int
	condition xml.Name
	reason    string
}

func unsupportedFilter() *filterFailure {
	return &filterFailure{status: http.StatusForbidden, condition: calName("supported-filter")}
}

func badFilter(reason string) *filterFailure {
	return &filterFailure{status: http.StatusBadRequest, reason: reason}
}

// calendarFilterFrom convierte el filtro de un calendar-query. Lo que el servidor no sabe evaluar (param-filter,
// time-range de una propiedad, comp-filter de tercer nivel, componentes que no guarda, elementos desconocidos)
// se rechaza con supported-filter en vez de ignorarse.
func calendarFilterFrom(req *calFilterReq) (domain.CalendarFilter, *filterFailure) {
	if req == nil || len(req.Comps) == 0 {
		return domain.CalendarFilter{}, badFilter("calendar-query sin comp-filter")
	}
	if len(req.Comps) != 1 || len(req.Others) > 0 {
		return domain.CalendarFilter{}, unsupportedFilter()
	}
	top := req.Comps[0]
	if !strings.EqualFold(top.Name, "VCALENDAR") || top.IsNotDefined != nil || top.TimeRange != nil ||
		len(top.PropFilters) > 0 || len(top.Others) > 0 || len(top.Subs) > maxCompFilters {
		return domain.CalendarFilter{}, unsupportedFilter()
	}
	var out domain.CalendarFilter
	for _, sub := range top.Subs {
		cf, fail := compFilterFrom(sub)
		if fail != nil {
			return domain.CalendarFilter{}, fail
		}
		out.Comps = append(out.Comps, cf)
	}
	return out, nil
}

func compFilterFrom(sub calSubFilterReq) (domain.CompFilter, *filterFailure) {
	name := strings.ToUpper(strings.TrimSpace(sub.Name))
	switch name {
	case componentVEvent, "VTODO", "VJOURNAL", "VFREEBUSY":
	default:
		return domain.CompFilter{}, unsupportedFilter()
	}
	if len(sub.Nested) > 0 || len(sub.Others) > 0 || len(sub.PropFilters) > maxPropFilters {
		return domain.CompFilter{}, unsupportedFilter()
	}
	cf := domain.CompFilter{Name: name, IsNotDefined: sub.IsNotDefined != nil}
	if cf.IsNotDefined && (sub.TimeRange != nil || len(sub.PropFilters) > 0) {
		return domain.CompFilter{}, badFilter("is-not-defined no admite más condiciones")
	}
	if sub.TimeRange != nil {
		tr, err := domain.ParseTimeRange(sub.TimeRange.Start, sub.TimeRange.End)
		if err != nil {
			return domain.CompFilter{}, badFilter("time-range no válido: fechas y horas en UTC, con al menos un extremo")
		}
		cf.Range = &tr
	}
	for _, pf := range sub.PropFilters {
		if len(pf.ParamFilters) > 0 || pf.TimeRange != nil || len(pf.Others) > 0 {
			return domain.CompFilter{}, unsupportedFilter()
		}
		f, cond, ok := propFilterFrom(pf.Name, pf.Test, pf.IsNotDefined != nil, pf.TextMatches, calName)
		if !ok {
			return domain.CompFilter{}, &filterFailure{status: http.StatusForbidden, condition: cond}
		}
		cf.Props = append(cf.Props, f)
	}
	return cf, nil
}
