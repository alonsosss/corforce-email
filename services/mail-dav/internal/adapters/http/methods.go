package http

import (
	"encoding/xml"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
)

// preconditionFrom reduce If-Match e If-None-Match a los valores de etag. If-Match compara de forma
// fuerte: un etag debil (W/) nunca coincide. If-None-Match compara de forma debil.
func preconditionFrom(h http.Header) domain.Precondition {
	var c domain.Precondition
	for _, raw := range h.Values("If-Match") {
		for _, tag := range strings.Split(raw, ",") {
			tag = strings.TrimSpace(tag)
			switch {
			case tag == "*":
				c.IfMatchAny = true
			case strings.HasPrefix(tag, "W/"):
			case tag != "":
				c.IfMatch = append(c.IfMatch, strings.Trim(tag, `"`))
			}
		}
	}
	for _, raw := range h.Values("If-None-Match") {
		for _, tag := range strings.Split(raw, ",") {
			tag = strings.TrimSpace(tag)
			if tag == "*" {
				c.IfNoneMatchAny = true
			} else if tag != "" {
				c.IfNoneMatch = append(c.IfNoneMatch, strings.Trim(strings.TrimPrefix(tag, "W/"), `"`))
			}
		}
	}
	// Un If-Match que solo trae etags debiles no puede coincidir con nada: una lista con un etag vacio
	// mantiene la condicion (y falla) en vez de dejarla sin efecto.
	if len(h.Values("If-Match")) > 0 && !c.IfMatchAny && len(c.IfMatch) == 0 {
		c.IfMatch = []string{""}
	}
	return c
}

// object es el contenido de un recurso que se sirve con GET: un vCard o un iCalendar.
type object struct {
	etag        string
	updated     time.Time
	contentType string
	body        string
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request, p domain.Principal, t target) {
	var obj object
	switch t.kind {
	case kindContact:
		c, err := h.uc.Contact(r.Context(), p, t.slug, t.resource)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		obj = object{etag: c.ETag, updated: c.UpdatedAt, contentType: vcardContentType, body: c.VCard}
	case kindEvent:
		e, err := h.uc.Event(r.Context(), p, t.slug, t.resource)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		obj = object{etag: e.ETag, updated: e.UpdatedAt, contentType: icalContentType, body: e.ICal}
	default:
		w.Header().Set("Allow", allowedMethods)
		http.Error(w, "solo se pueden leer contactos y eventos con GET", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("ETag", etagHeader(obj.etag))
	w.Header().Set("Last-Modified", obj.updated.UTC().Format(http.TimeFormat))
	cond := preconditionFrom(r.Header)
	unchanged := domain.Precondition{IfNoneMatchAny: cond.IfNoneMatchAny, IfNoneMatch: cond.IfNoneMatch}
	if (cond.IfNoneMatchAny || len(cond.IfNoneMatch) > 0) && unchanged.Check(&obj.etag) != nil {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", obj.contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(obj.body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, obj.body)
	}
}

// vcardMediaType admite los tipos con los que los clientes envian un vCard, y solo UTF-8.
func vcardMediaType(header string) bool {
	if strings.TrimSpace(header) == "" {
		return true
	}
	media, params, err := mime.ParseMediaType(header)
	if err != nil {
		return false
	}
	if cs, ok := params["charset"]; ok && !strings.EqualFold(cs, "utf-8") {
		return false
	}
	switch strings.ToLower(media) {
	case "text/vcard", "text/x-vcard", "text/directory":
		return true
	}
	return false
}

func (h *Handler) put(w http.ResponseWriter, r *http.Request, p domain.Principal, t target) {
	if t.kind == kindEvent {
		h.putEvent(w, r, p, t)
		return
	}
	if t.kind != kindContact {
		w.Header().Set("Allow", allowedMethods)
		http.Error(w, "solo se pueden guardar contactos y eventos con PUT", http.StatusMethodNotAllowed)
		return
	}
	if !vcardMediaType(r.Header.Get("Content-Type")) {
		writeDAVError(w, http.StatusUnsupportedMediaType, cardName("supported-address-data"))
		return
	}
	limit := int64(h.uc.Limits().MaxVCardBytes)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "el vCard supera el tamano maximo", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "no se pudo leer el cuerpo", http.StatusBadRequest)
		return
	}
	etag, created, err := h.uc.Put(r.Context(), p, t.slug, t.resource, string(body), preconditionFrom(r.Header))
	var conflict *domain.UIDConflictError
	if errors.As(err, &conflict) {
		var children []element
		if conflict.Resource != "" {
			children = append(children, hrefEl(h.contactPath(p, t.slug, conflict.Resource)))
		}
		writeDAVError(w, http.StatusConflict, cardName("no-uid-conflict"), children...)
		return
	}
	if errors.Is(err, domain.ErrNotFound) {
		http.Error(w, "la libreta no existe", http.StatusConflict)
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

func (h *Handler) delete(w http.ResponseWriter, r *http.Request, p domain.Principal, t target) {
	var err error
	switch t.kind {
	case kindContact:
		err = h.uc.Delete(r.Context(), p, t.slug, t.resource, preconditionFrom(r.Header))
	case kindBook:
		err = h.uc.DeleteAddressbook(r.Context(), p, t.slug)
	case kindEvent:
		err = h.uc.DeleteEvent(r.Context(), p, t.slug, t.resource, preconditionFrom(r.Header))
	case kindCalendar:
		err = h.uc.DeleteCalendar(r.Context(), p, t.slug)
	default:
		http.Error(w, "este recurso no se puede borrar", http.StatusForbidden)
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// mkcol crea una libreta (MKCOL extendido de RFC 5689, o simple) o, en la ruta de un calendario, un
// calendario. En una libreta solo se admite el tipo addressbook; en un calendario, el tipo calendar.
func (h *Handler) mkcol(w http.ResponseWriter, r *http.Request, p domain.Principal, t target) {
	if t.kind != kindBook && t.kind != kindCalendar {
		http.Error(w, "solo se pueden crear libretas y calendarios", http.StatusForbidden)
		return
	}
	var req mkcolReq
	err := decodeDoc(http.MaxBytesReader(w, r.Body, h.cfg.MaxXMLBytes), &req)
	if err != nil && !errors.Is(err, errEmptyBody) {
		badBody(w, err)
		return
	}
	if t.kind == kindCalendar {
		h.createCalendar(w, r, p, t, req.Set, true)
		return
	}
	var displayName, description string
	for _, set := range req.Set {
		if rt := set.Prop.ResourceType; rt != nil && !hasName(rt.Items, cardName("addressbook")) {
			writeDAVError(w, http.StatusForbidden, davName("valid-resourcetype"))
			return
		}
		if set.Prop.DisplayName != nil {
			displayName = *set.Prop.DisplayName
		}
		if set.Prop.AddressbookDescription != nil {
			description = *set.Prop.AddressbookDescription
		}
	}
	if _, err := h.uc.CreateAddressbook(r.Context(), p, t.slug, displayName, description); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func hasName(items []xmlName, want xml.Name) bool {
	for _, i := range items {
		if i.XMLName == want {
			return true
		}
	}
	return false
}
