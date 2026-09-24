package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/contacts/internal/app"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

const (
	// submitBodyLimit acota un envio publico: 20 campos de hasta 1000 caracteres y el token.
	submitBodyLimit = 32 << 10
	// Nombres de los campos propios del formulario HTML. Los del contacto van como field.<clave>
	// y no chocan con estos.
	formTokenField    = "_token"
	formConsentField  = "consent"
	formHoneypotField = "homepage"
	formFieldPrefix   = "field."
	// corsMaxAge es cuanto recuerda el navegador una comprobacion previa.
	corsMaxAge = "600"
	// embedScriptMaxAge es la cache del script que incrusta el iframe: no lleva token.
	embedScriptMaxAge = "public, max-age=300"
)

// PublicFormRoutes cuelga de /api/v1/public/contacts/forms: sin sesion, por el gateway. La
// empresa y el formulario salen de la clave publica; el envio lo protegen el token firmado con
// tiempo minimo, el origen, el campo trampa y los cupos por IP y por formulario.
func (h *Handler) PublicFormRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(withPublicDeadline)
	r.Get("/{form}", h.PublicFormDefinition)
	r.Options("/{form}", h.FormPreflight)
	r.Get("/{form}/embed", h.FormEmbed)
	r.Get("/{form}/embed.js", h.FormEmbedScript)
	r.Post("/{form}/submit", h.SubmitForm)
	r.Options("/{form}/submit", h.FormPreflight)
	return r
}

// publicFormError escribe el error de una ruta publica en JSON.
func publicFormError(w http.ResponseWriter, err error) {
	var limited *app.RateLimitedError
	switch {
	case errors.As(err, &limited):
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(math.Max(limited.RetryAfter.Seconds(), 1)))))
		response.Err(w, http.StatusTooManyRequests, "RATE_LIMITED", limited.Error())
	case errors.Is(err, domain.ErrFormNotFound):
		response.Err(w, http.StatusNotFound, "FORM_NOT_FOUND", "formulario no disponible")
	case errors.Is(err, errOriginNotAllowed):
		response.Err(w, http.StatusForbidden, "ORIGIN_NOT_ALLOWED", errOriginNotAllowed.Error())
	case errors.Is(err, app.ErrFormTokenInvalid):
		response.Err(w, http.StatusBadRequest, "FORM_TOKEN_INVALID", app.ErrFormTokenInvalid.Error())
	case errors.Is(err, domain.ErrInvalidSubmission), errors.Is(err, domain.ErrConsentNotAccepted):
		response.ErrValidation(w, err.Error())
	case errors.Is(err, app.ErrSuppressionUnavailable), errors.Is(err, app.ErrFormsUnavailable), errors.Is(err, errTenantUnavailable):
		response.Err(w, http.StatusServiceUnavailable, "FORMS_UNAVAILABLE", "no se pudo registrar la suscripcion en este momento; vuelve a intentarlo en unos minutos")
	default:
		response.Unexpected(w, err)
	}
}

var (
	errOriginNotAllowed  = errors.New("este sitio no puede usar el formulario")
	errTenantUnavailable = errors.New("base de la empresa no disponible")
)

// publicForm resuelve la base de la empresa de la clave y el formulario activo. Una clave mal
// formada, una empresa que no existe o un formulario desactivado son el mismo 404.
func (h *Handler) publicForm(r *http.Request) (context.Context, *domain.SubscriptionForm, domain.Definitions, error) {
	tenantID, formID, ok := parseFormKey(chi.URLParam(r, "form"))
	if !ok {
		return nil, nil, nil, domain.ErrFormNotFound
	}
	pool, err := h.tenantDB.ResolveForTenant(r.Context(), tenantID.String())
	if err != nil {
		if db.IsUnknownTenant(err) {
			return nil, nil, nil, domain.ErrFormNotFound
		}
		h.logger.Error("contacts: base de la empresa no disponible para un formulario publico", zap.Error(err))
		return nil, nil, nil, errTenantUnavailable
	}
	ctx := db.WithTenant(r.Context(), pool, tenantID.String())
	f, defs, err := h.uc.PublicForm(ctx, tenantID, formID)
	if err != nil {
		return nil, nil, nil, err
	}
	return ctx, f, defs, nil
}

// checkOrigin aplica la politica de origen: una peticion del navegador (con Origin) solo pasa
// desde la plataforma o un origen declarado, y entonces lleva CORS sin credenciales. Sin Origin
// no hay navegador que proteger (el resto de frenos sigue aplicando). Origin "null" (un marco
// aislado, un fichero local) no es ningun origen declarado.
func (h *Handler) checkOrigin(w http.ResponseWriter, r *http.Request, f *domain.SubscriptionForm) error {
	w.Header().Add("Vary", "Origin")
	raw := r.Header.Get("Origin")
	if raw == "" {
		return nil
	}
	origin := app.OriginOf(raw)
	if !f.AllowsOrigin(origin, h.platformOrigin) {
		return errOriginNotAllowed
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	return nil
}

// FormPreflight responde la comprobacion previa de CORS de la definicion y del envio.
func (h *Handler) FormPreflight(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	_, f, _, err := h.publicForm(r)
	if err != nil {
		publicFormError(w, err)
		return
	}
	if err := h.checkOrigin(w, r, f); err != nil {
		publicFormError(w, err)
		return
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Access-Control-Max-Age", corsMaxAge)
	w.WriteHeader(http.StatusNoContent)
}

type publicFormField struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Placeholder string `json:"placeholder"`
}

type publicFormDefinition struct {
	Key             string            `json:"key"`
	Title           string            `json:"title"`
	Description     string            `json:"description"`
	SubmitLabel     string            `json:"submit_label"`
	ConsentText     string            `json:"consent_text"`
	Fields          []publicFormField `json:"fields"`
	Token           string            `json:"token"`
	MinFillSeconds  int               `json:"min_fill_seconds"`
	TokenTTLSeconds int               `json:"token_ttl_seconds"`
	SubmitURL       string            `json:"submit_url"`
}

func publicFields(f *domain.SubscriptionForm, defs domain.Definitions) []publicFormField {
	active := f.ActiveFields(defs)
	out := make([]publicFormField, 0, len(active))
	for _, fd := range active {
		typ, _ := domain.FieldType(fd.Key, defs)
		out = append(out, publicFormField{Key: fd.Key, Label: fd.Label, Type: typ, Required: fd.Required, Placeholder: fd.Placeholder})
	}
	return out
}

// PublicFormDefinition es la definicion para una integracion propia (el sitio de la empresa
// pinta el formulario y envia por fetch), con el token con que se envia.
func (h *Handler) PublicFormDefinition(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	_, f, defs, err := h.publicForm(r)
	if err != nil {
		publicFormError(w, err)
		return
	}
	if err := h.checkOrigin(w, r, f); err != nil {
		publicFormError(w, err)
		return
	}
	token, err := h.uc.FormToken(f)
	if err != nil {
		publicFormError(w, err)
		return
	}
	cfg := h.uc.FormPublicConfig()
	key := FormKey(f.TenantID, f.ID)
	response.JSON(w, http.StatusOK, publicFormDefinition{
		Key: key, Title: f.Texts.Title, Description: f.Texts.Description, SubmitLabel: submitLabel(f),
		ConsentText: f.Texts.ConsentText, Fields: publicFields(f, defs), Token: token,
		MinFillSeconds: int(cfg.MinFill.Seconds()), TokenTTLSeconds: int(cfg.TokenTTL.Seconds()),
		SubmitURL: h.formPublicURL(key, "/submit"),
	})
}

// FormEmbed es la pagina del iframe: el formulario en HTML sin JavaScript, que envia por POST a
// la misma plataforma. Solo se deja incrustar en la plataforma y en los origenes declarados.
func (h *Handler) FormEmbed(w http.ResponseWriter, r *http.Request) {
	_, f, defs, err := h.publicForm(r)
	if err != nil {
		h.writeFormUnavailable(w, err)
		return
	}
	h.writeEmbed(w, http.StatusOK, f, defs, "", nil)
}

func (h *Handler) writeEmbed(w http.ResponseWriter, status int, f *domain.SubscriptionForm, defs domain.Definitions, errMsg string, values map[string]string) {
	token, err := h.uc.FormToken(f)
	if err != nil {
		h.writeFormUnavailable(w, err)
		return
	}
	key := FormKey(f.TenantID, f.ID)
	data := embedPage{
		Title: f.Texts.Title, Description: f.Texts.Description, SubmitLabel: submitLabel(f),
		ConsentText: f.Texts.ConsentText, Action: h.formPublicURL(key, "/submit"), Token: token,
		TopTarget: f.RedirectURL != nil, Error: errMsg,
		TokenField: formTokenField, ConsentField: formConsentField, HoneypotField: formHoneypotField,
	}
	for _, fd := range publicFields(f, defs) {
		data.Fields = append(data.Fields, embedField{
			Name: formFieldPrefix + fd.Key, Label: fd.Label, Required: fd.Required, Placeholder: fd.Placeholder,
			Input: inputType(fd.Type), Autocomplete: autocompleteFor(fd.Key), Value: values[fd.Key],
			Checked: fd.Type == string(domain.AttrBoolean) && values[fd.Key] != "",
		})
	}
	h.writeFormPage(w, status, f, embedTemplate, data)
}

func (h *Handler) writeFormUnavailable(w http.ResponseWriter, err error) {
	status := http.StatusNotFound
	msg := messagePage{Title: "Formulario no disponible", Body: "Este formulario ya no está disponible."}
	if !errors.Is(err, domain.ErrFormNotFound) {
		status = http.StatusServiceUnavailable
		msg = messagePage{Title: "Servicio no disponible", Body: "No se pudo cargar el formulario en este momento. Vuelve a intentarlo en unos minutos."}
		if !errors.Is(err, errTenantUnavailable) && !errors.Is(err, app.ErrFormsUnavailable) {
			h.logger.Error("contacts: formulario publico no disponible", zap.Error(err))
		}
	}
	h.writeFormPage(w, status, nil, messageTemplate, msg)
}

// formCSP es la politica de las paginas del formulario: sin scripts, estilos en linea, envio
// solo a la plataforma (y al destino de la redireccion) y marco solo en la plataforma y en los
// origenes declarados. El gateway la conserva (content embeddable_html) en lugar de la suya.
func (h *Handler) formCSP(f *domain.SubscriptionForm) string {
	ancestors := "'self'"
	formAction := "'self'"
	if f != nil {
		for _, o := range f.AllowedOrigins {
			ancestors += " " + o
		}
		if f.RedirectURL != nil {
			if o := app.OriginOf(*f.RedirectURL); o != "" {
				formAction += " " + o
			}
		}
	} else {
		ancestors = "'none'"
	}
	return "default-src 'none'; style-src 'unsafe-inline'; img-src data:; base-uri 'none'; " +
		"form-action " + formAction + "; frame-ancestors " + ancestors
}

func (h *Handler) writeFormPage(w http.ResponseWriter, status int, f *domain.SubscriptionForm, tmpl pageRenderer, data any) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		h.logger.Error("contacts: no se pudo pintar la pagina del formulario", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	hdr := w.Header()
	hdr.Set("Content-Type", "text/html; charset=utf-8")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("Content-Security-Policy", h.formCSP(f))
	hdr.Del("X-Frame-Options")
	hdr.Set("Referrer-Policy", "no-referrer")
	hdr.Set("X-Robots-Tag", "noindex, nofollow")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// FormEmbedScript inserta el iframe del formulario justo despues de la etiqueta <script> que lo
// carga. data-height en esa etiqueta fija la altura.
func (h *Handler) FormEmbedScript(w http.ResponseWriter, r *http.Request) {
	_, f, defs, err := h.publicForm(r)
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		publicFormError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", embedScriptMaxAge)
	_, _ = io.WriteString(w, h.embedScriptFor(f, defs))
}

// embedScriptFor arma el script: la direccion y el titulo van como literales JSON, que
// encoding/json escapa tambien para HTML (<, > y &), asi que no pueden cerrar la etiqueta.
func (h *Handler) embedScriptFor(f *domain.SubscriptionForm, defs domain.Definitions) string {
	src, _ := json.Marshal(h.formPublicURL(FormKey(f.TenantID, f.ID), "/embed"))
	title := f.Texts.Title
	if title == "" {
		title = f.Name
	}
	titleJSON, _ := json.Marshal(title)
	return fmt.Sprintf(embedScript, src, titleJSON, embedHeight(f, defs))
}

// embedHeight estima la altura del iframe: cabecera, cada campo y el consentimiento.
func embedHeight(f *domain.SubscriptionForm, defs domain.Definitions) int {
	h := 180
	if f.Texts.Title != "" {
		h += 40
	}
	if f.Texts.Description != "" {
		h += 60
	}
	h += 78 * len(publicFields(f, defs))
	h += 20 * (1 + len(f.Texts.ConsentText)/60)
	return h
}

const embedScript = `(function(){var s=document.currentScript;if(!s||!s.parentNode)return;` +
	`var f=document.createElement("iframe");f.src=%s;f.title=%s;f.loading="lazy";` +
	`var h=parseInt(s.getAttribute("data-height")||"",10);f.style.width="100%%";f.style.border="0";` +
	`f.style.height=(h>0?h:%d)+"px";f.setAttribute("sandbox","allow-forms allow-same-origin allow-top-navigation-by-user-activation");` +
	`s.parentNode.insertBefore(f,s.nextSibling);})();`

func submitLabel(f *domain.SubscriptionForm) string {
	if f.Texts.SubmitLabel != "" {
		return f.Texts.SubmitLabel
	}
	return "Suscribirme"
}

func inputType(fieldType string) string {
	switch fieldType {
	case domain.FieldTypeEmail:
		return "email"
	case string(domain.AttrNumber):
		return "number"
	case string(domain.AttrDate):
		return "date"
	case string(domain.AttrBoolean):
		return "checkbox"
	}
	return "text"
}

func autocompleteFor(key string) string {
	switch key {
	case domain.FieldEmail:
		return "email"
	case domain.FieldFirstName:
		return "given-name"
	case domain.FieldLastName:
		return "family-name"
	}
	return "off"
}

// submitMode es como llego el envio: por fetch (JSON, responde JSON) o por el formulario HTML
// del iframe (responde una pagina o redirige).
type submitMode int

const (
	submitJSON submitMode = iota
	submitHTML
)

type submitRequest struct {
	Token    string                     `json:"token"`
	Fields   map[string]json.RawMessage `json:"fields"`
	Consent  bool                       `json:"consent"`
	Honeypot string                     `json:"homepage"`
}

// SubmitForm recibe un envio publico. La respuesta de exito es la misma exista o no la
// direccion, este excluida o ya suscrita, o haya caido en el campo trampa.
func (h *Handler) SubmitForm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	mode, ok := submitModeOf(r)
	if !ok {
		response.Err(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "el envio va en JSON o como formulario HTML")
		return
	}
	ip := r.Header.Get("X-Real-IP")
	if err := h.uc.CheckSubmitRate(r.Context(), ip); err != nil {
		h.submitFailed(w, mode, nil, nil, nil, err)
		return
	}
	ctx, f, defs, err := h.publicForm(r)
	if err != nil {
		h.submitFailed(w, mode, nil, nil, nil, err)
		return
	}
	if err := h.checkOrigin(w, r, f); err != nil {
		h.submitFailed(w, mode, f, defs, nil, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, submitBodyLimit)
	in, err := decodeSubmission(r, mode)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			response.Err(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "el envio supera el tamano maximo")
			return
		}
		h.submitFailed(w, mode, f, defs, nil, fmt.Errorf("%w: %v", domain.ErrInvalidSubmission, err))
		return
	}
	in.IP, in.UserAgent = ip, r.UserAgent()
	in.Origin = app.OriginOf(r.Header.Get("Origin"))
	res, err := h.uc.SubmitForm(ctx, f, defs, in)
	if err != nil {
		h.submitFailed(w, mode, f, defs, in.Strings, err)
		return
	}
	if !res.Ignored {
		h.logger.Info("contacts: envio de formulario", zap.String("tenant_id", f.TenantID.String()),
			zap.String("form_id", f.ID.String()), zap.String("outcome", string(res.Outcome)))
	}
	h.submitAccepted(w, mode, f)
}

func submitModeOf(r *http.Request) (submitMode, bool) {
	ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return 0, false
	}
	switch ct {
	case "application/json":
		return submitJSON, true
	case "application/x-www-form-urlencoded":
		return submitHTML, true
	}
	return 0, false
}

func decodeSubmission(r *http.Request, mode submitMode) (app.SubmitInput, error) {
	if mode == submitJSON {
		var req submitRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			return app.SubmitInput{}, err
		}
		if dec.More() {
			return app.SubmitInput{}, errors.New("el cuerpo debe ser un solo objeto JSON")
		}
		if req.Fields == nil {
			req.Fields = map[string]json.RawMessage{}
		}
		return app.SubmitInput{Token: req.Token, Values: req.Fields, Consent: req.Consent, Honeypot: req.Honeypot}, nil
	}
	if err := r.ParseForm(); err != nil {
		return app.SubmitInput{}, err
	}
	in := app.SubmitInput{
		Token: r.PostForm.Get(formTokenField), Honeypot: r.PostForm.Get(formHoneypotField),
		Consent: r.PostForm.Get(formConsentField) != "", Strings: map[string]string{},
	}
	for name, vals := range r.PostForm {
		key, found := strings.CutPrefix(name, formFieldPrefix)
		if !found || len(vals) == 0 {
			continue
		}
		if len(vals) > 1 {
			return app.SubmitInput{}, fmt.Errorf("el campo %s va repetido", key)
		}
		in.Strings[key] = vals[0]
	}
	return in, nil
}

func (h *Handler) submitAccepted(w http.ResponseWriter, mode submitMode, f *domain.SubscriptionForm) {
	if mode == submitJSON {
		response.JSON(w, http.StatusAccepted, map[string]any{
			"status": "received", "message": f.Texts.SuccessMessage, "redirect_url": f.RedirectURL,
		})
		return
	}
	if f.RedirectURL != nil {
		w.Header().Set("Content-Security-Policy", h.formCSP(f))
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Location", *f.RedirectURL)
		w.WriteHeader(http.StatusSeeOther)
		return
	}
	title := f.Texts.Title
	if title == "" {
		title = "Gracias"
	}
	h.writeFormPage(w, http.StatusOK, f, messageTemplate, messagePage{Title: title, Body: f.Texts.SuccessMessage})
}

// submitFailed responde el fallo: JSON por fetch; por el formulario HTML, el mismo formulario
// con el aviso (y lo escrito) cuando se puede corregir, o una pagina con el motivo.
func (h *Handler) submitFailed(w http.ResponseWriter, mode submitMode, f *domain.SubscriptionForm, defs domain.Definitions, values map[string]string, err error) {
	if mode == submitJSON {
		publicFormError(w, err)
		return
	}
	var limited *app.RateLimitedError
	switch {
	case f != nil && (errors.Is(err, domain.ErrInvalidSubmission) || errors.Is(err, domain.ErrConsentNotAccepted) || errors.Is(err, app.ErrFormTokenInvalid)):
		h.writeEmbed(w, http.StatusUnprocessableEntity, f, defs, formErrorMessage(err), values)
	case errors.As(err, &limited):
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(math.Max(limited.RetryAfter.Seconds(), 1)))))
		h.writeFormPage(w, http.StatusTooManyRequests, f, messageTemplate, messagePage{Title: "Demasiados envíos", Body: "Vuelve a intentarlo en unos minutos."})
	case errors.Is(err, errOriginNotAllowed):
		h.writeFormPage(w, http.StatusForbidden, nil, messageTemplate, messagePage{Title: "Formulario no disponible", Body: errOriginNotAllowed.Error()})
	case errors.Is(err, domain.ErrFormNotFound):
		h.writeFormUnavailable(w, err)
	case errors.Is(err, app.ErrSuppressionUnavailable), errors.Is(err, app.ErrFormsUnavailable), errors.Is(err, errTenantUnavailable):
		h.writeFormUnavailable(w, err)
	default:
		h.logger.Error("contacts: el envio del formulario no se pudo registrar", zap.Error(err))
		h.writeFormPage(w, http.StatusInternalServerError, f, messageTemplate, messagePage{Title: "Servicio no disponible", Body: "No se pudo registrar la suscripción. Vuelve a intentarlo en unos minutos."})
	}
}

// formErrorMessage quita el prefijo generico del error de dominio: la persona ve el motivo.
func formErrorMessage(err error) string {
	msg := err.Error()
	if rest, ok := strings.CutPrefix(msg, domain.ErrInvalidSubmission.Error()+": "); ok {
		return rest
	}
	return msg
}

// publicDeadline acota lo que espera una peticion publica a la base y a suppression.
const publicDeadline = 10 * time.Second

func withPublicDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), publicDeadline)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
