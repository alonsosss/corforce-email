package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// FormStatus dice si un formulario de suscripcion admite envios.
type FormStatus string

const (
	FormActive   FormStatus = "active"
	FormDisabled FormStatus = "disabled"
)

func FormStatuses() []FormStatus { return []FormStatus{FormActive, FormDisabled} }

// Campos fijos del contacto que un formulario puede pedir; el resto son atributos declarados.
const (
	FieldEmail     = "email"
	FieldFirstName = "first_name"
	FieldLastName  = "last_name"
)

// FieldTypeEmail es el tipo de entrada de la direccion; los demas son los de AttrType.
const FieldTypeEmail = "email"

// BuiltinFormFields son los campos fijos que puede pedir un formulario, con su tipo de entrada.
func BuiltinFormFields() []FormFieldType {
	return []FormFieldType{
		{Key: FieldEmail, Type: FieldTypeEmail},
		{Key: FieldFirstName, Type: string(AttrString)},
		{Key: FieldLastName, Type: string(AttrString)},
	}
}

// FormFieldType es la clave de un campo con el tipo de entrada que le corresponde.
type FormFieldType struct {
	Key  string `json:"key"`
	Type string `json:"type"`
}

// Topes de la definicion de un formulario. Los de texto se repiten en la migracion.
const (
	MaxFormFields              = 20
	MaxFormNameLength          = 200
	MaxFormLabelLength         = 200
	MaxFormPlaceholderLength   = 200
	MaxFormTitleLength         = 200
	MaxFormDescriptionLength   = 2000
	MaxFormSubmitLabelLength   = 60
	MaxFormConsentTextLength   = 2000
	MaxFormSuccessMessageLen   = 1000
	MaxFormAllowedOrigins      = 20
	MaxFormRedirectURLLength   = 2048
	maxFormOriginLength        = 300
	formSubmissionSourcePrefix = "form:"
)

var (
	ErrFormNotFound = errors.New("formulario no encontrado")
	ErrFormExists   = errors.New("ya existe un formulario con ese nombre")
	// ErrListInUseByForm: una lista destino de un formulario no se borra; quien confirme
	// despues no tendria donde entrar.
	ErrListInUseByForm = errors.New("la lista es la destino de al menos un formulario de suscripcion")
	ErrInvalidForm     = errors.New("formulario no valido")
	// ErrInvalidSubmission: los datos enviados por un formulario publico no son validos.
	ErrInvalidSubmission = errors.New("datos del formulario no validos")
	// ErrConsentNotAccepted: el envio no marca la casilla del texto de consentimiento.
	ErrConsentNotAccepted = errors.New("hay que aceptar el texto de consentimiento para suscribirse")
)

// FormField es un campo del formulario en su orden de presentacion.
type FormField struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Required    bool   `json:"required"`
	Placeholder string `json:"placeholder"`
}

// FormTexts son los textos que ve quien se suscribe. ConsentText es lo que acepta y queda
// como evidencia del consentimiento.
type FormTexts struct {
	Title          string `json:"title"`
	Description    string `json:"description"`
	SubmitLabel    string `json:"submit_label"`
	ConsentText    string `json:"consent_text"`
	SuccessMessage string `json:"success_message"`
}

// SubscriptionForm es un formulario de suscripcion incrustable. El doble opt-in es
// obligatorio: no hay forma de desactivarlo.
type SubscriptionForm struct {
	ID             uuid.UUID   `json:"id"`
	TenantID       uuid.UUID   `json:"tenant_id"`
	Name           string      `json:"name"`
	Status         FormStatus  `json:"status"`
	ListID         uuid.UUID   `json:"list_id"`
	Fields         []FormField `json:"fields"`
	Texts          FormTexts   `json:"texts"`
	RedirectURL    *string     `json:"redirect_url"`
	AllowedOrigins []string    `json:"allowed_origins"`
	CreatedBy      uuid.UUID   `json:"created_by"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

func invalidForm(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidForm, fmt.Sprintf(format, args...))
}

// Normalize valida la definicion contra los atributos declarados y la deja en su forma
// canonica: textos recortados, email presente y obligatorio, origenes normalizados.
func (f *SubscriptionForm) Normalize(defs Definitions) error {
	var err error
	if f.Name, err = formText("name", f.Name, MaxFormNameLength, true); err != nil {
		return err
	}
	if f.Status == "" {
		f.Status = FormActive
	}
	if f.Status != FormActive && f.Status != FormDisabled {
		return invalidForm("status debe ser active o disabled")
	}
	if f.ListID == uuid.Nil {
		return invalidForm("list_id es obligatorio")
	}
	if err := f.normalizeFields(defs); err != nil {
		return err
	}
	if err := f.Texts.normalize(); err != nil {
		return err
	}
	if f.RedirectURL != nil {
		u, err := NormalizeRedirectURL(*f.RedirectURL)
		if err != nil {
			return err
		}
		f.RedirectURL = u
	}
	origins, err := NormalizeOrigins(f.AllowedOrigins)
	if err != nil {
		return err
	}
	f.AllowedOrigins = origins
	return nil
}

func (f *SubscriptionForm) normalizeFields(defs Definitions) error {
	if len(f.Fields) == 0 || len(f.Fields) > MaxFormFields {
		return invalidForm("fields admite de 1 a %d campos", MaxFormFields)
	}
	seen := make(map[string]bool, len(f.Fields))
	for i := range f.Fields {
		fd := &f.Fields[i]
		fd.Key = strings.TrimSpace(fd.Key)
		if _, err := FieldType(fd.Key, defs); err != nil {
			return err
		}
		if seen[fd.Key] {
			return invalidForm("el campo %s esta repetido", fd.Key)
		}
		seen[fd.Key] = true
		var err error
		if fd.Label, err = formText("fields."+fd.Key+".label", fd.Label, MaxFormLabelLength, true); err != nil {
			return err
		}
		if fd.Placeholder, err = formText("fields."+fd.Key+".placeholder", fd.Placeholder, MaxFormPlaceholderLength, false); err != nil {
			return err
		}
		if fd.Key == FieldEmail {
			fd.Required = true
		}
	}
	if !seen[FieldEmail] {
		return invalidForm("el formulario tiene que pedir el campo email")
	}
	for key, def := range defs {
		if !def.Required {
			continue
		}
		if !seen[key] {
			return invalidForm("el atributo %s es obligatorio en los contactos: el formulario tiene que pedirlo", key)
		}
		for _, fd := range f.Fields {
			if fd.Key == key && !fd.Required {
				return invalidForm("el atributo %s es obligatorio en los contactos: su campo tiene que ser obligatorio", key)
			}
		}
	}
	return nil
}

func (t *FormTexts) normalize() error {
	var err error
	if t.Title, err = formText("texts.title", t.Title, MaxFormTitleLength, false); err != nil {
		return err
	}
	if t.Description, err = formMultiline("texts.description", t.Description, MaxFormDescriptionLength, false); err != nil {
		return err
	}
	if t.SubmitLabel, err = formText("texts.submit_label", t.SubmitLabel, MaxFormSubmitLabelLength, false); err != nil {
		return err
	}
	if t.ConsentText, err = formMultiline("texts.consent_text", t.ConsentText, MaxFormConsentTextLength, true); err != nil {
		return err
	}
	if t.SuccessMessage, err = formMultiline("texts.success_message", t.SuccessMessage, MaxFormSuccessMessageLen, true); err != nil {
		return err
	}
	return nil
}

// formText recorta un texto de una linea y comprueba su longitud en caracteres.
func formText(field, raw string, max int, required bool) (string, error) {
	s := strings.TrimSpace(raw)
	if required && s == "" {
		return "", invalidForm("%s es obligatorio", field)
	}
	if utf8.RuneCountInString(s) > max || !utf8.ValidString(s) || strings.ContainsAny(s, "\x00\r\n") {
		return "", invalidForm("%s admite una linea de como maximo %d caracteres", field, max)
	}
	return s, nil
}

// formMultiline admite saltos de linea (\r\n se normaliza a \n).
func formMultiline(field, raw string, max int, required bool) (string, error) {
	s := strings.TrimSpace(strings.ReplaceAll(raw, "\r\n", "\n"))
	if required && s == "" {
		return "", invalidForm("%s es obligatorio", field)
	}
	if utf8.RuneCountInString(s) > max || !utf8.ValidString(s) || strings.ContainsAny(s, "\x00\r") {
		return "", invalidForm("%s admite como maximo %d caracteres", field, max)
	}
	return s, nil
}

// FieldType es el tipo de entrada de un campo: email, los campos de nombre como string y los
// atributos con su tipo declarado. Una clave que no es ninguno de ellos no es un campo valido.
func FieldType(key string, defs Definitions) (string, error) {
	switch key {
	case FieldEmail:
		return FieldTypeEmail, nil
	case FieldFirstName, FieldLastName:
		return string(AttrString), nil
	}
	def, ok := defs[key]
	if !ok {
		return "", invalidForm("el campo %s no es email, first_name, last_name ni un atributo declarado", truncate(key, 63))
	}
	return string(def.Type), nil
}

// NormalizeRedirectURL acepta una URL https absoluta sin credenciales ni fragmento. Vacia = sin
// redireccion (nil).
func NormalizeRedirectURL(raw string) (*string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" ||
		len(s) > MaxFormRedirectURLLength || strings.ContainsAny(s, " \t\r\n\"'<>\\") {
		return nil, invalidForm("redirect_url debe ser una URL https absoluta, sin credenciales ni fragmento, de hasta %d caracteres", MaxFormRedirectURLLength)
	}
	out := u.String()
	return &out, nil
}

// NormalizeOrigin reduce un origen a https://host[:puerto] en minusculas. Sin comodines: cada
// dominio que incrusta el formulario se declara.
func NormalizeOrigin(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	u, err := url.Parse(s)
	if err != nil || len(s) > maxFormOriginLength || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", invalidForm("allowed_origins: %q debe ser un origen https://dominio[:puerto] sin ruta", truncate(s, 80))
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || strings.Contains(host, "*") || !validOriginHost(host) {
		return "", invalidForm("allowed_origins: %q no es un dominio valido", truncate(s, 80))
	}
	origin := "https://" + host
	if strings.Contains(host, ":") {
		origin = "https://[" + host + "]"
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", invalidForm("allowed_origins: %q lleva un puerto no valido", truncate(s, 80))
		}
		if n != 443 {
			origin += ":" + strconv.Itoa(n)
		}
	}
	return origin, nil
}

func validOriginHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return true
	}
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// NormalizeOrigins normaliza y deduplica la lista, conservando su orden.
func NormalizeOrigins(raw []string) ([]string, error) {
	if len(raw) > MaxFormAllowedOrigins {
		return nil, invalidForm("allowed_origins admite como maximo %d origenes", MaxFormAllowedOrigins)
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, r := range raw {
		o, err := NormalizeOrigin(r)
		if err != nil {
			return nil, err
		}
		if !seen[o] {
			seen[o] = true
			out = append(out, o)
		}
	}
	return out, nil
}

// AllowsOrigin dice si una peticion del navegador con ese Origin puede usar el formulario: el
// propio origen de la plataforma (el iframe y las paginas de aterrizaje) o uno declarado. La
// comparacion es exacta sobre la forma normalizada.
func (f *SubscriptionForm) AllowsOrigin(origin, platformOrigin string) bool {
	if origin == "" {
		return false
	}
	if platformOrigin != "" && origin == platformOrigin {
		return true
	}
	for _, o := range f.AllowedOrigins {
		if o == origin {
			return true
		}
	}
	return false
}

// Active dice si el formulario admite envios.
func (f *SubscriptionForm) Active() bool { return f.Status == FormActive }

// FormSubmission es el valor ya validado de un envio.
type FormSubmission struct {
	Email      string
	FirstName  string
	LastName   string
	Attributes RawAttributes
}

func invalidSubmission(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidSubmission, fmt.Sprintf(format, args...))
}

// ActiveFields son los campos que se pueden pedir hoy: un atributo retirado despues de guardar
// el formulario deja de pedirse (y lo que llegue para el se ignora) en lugar de bloquearlo.
func (f *SubscriptionForm) ActiveFields(defs Definitions) []FormField {
	out := make([]FormField, 0, len(f.Fields))
	for _, fd := range f.Fields {
		if _, err := FieldType(fd.Key, defs); err == nil {
			out = append(out, fd)
		}
	}
	return out
}

// checkSubmittedKeys rechaza un valor para un campo que el formulario nunca pidio.
func (f *SubscriptionForm) checkSubmittedKeys(keys []string) error {
	for _, key := range keys {
		known := false
		for _, fd := range f.Fields {
			if fd.Key == key {
				known = true
				break
			}
		}
		if !known {
			return invalidSubmission("el formulario no pide el campo %s", truncate(key, 63))
		}
	}
	return nil
}

// ParseSubmission valida los valores de un envio (cada uno en JSON) contra el formulario y los
// atributos declarados. Un campo que el formulario no pide se rechaza; uno opcional vacio se
// ignora. Los mensajes nombran la etiqueta del campo, que es lo que ve quien lo rellena.
func (f *SubscriptionForm) ParseSubmission(values map[string]json.RawMessage, defs Definitions) (*FormSubmission, error) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	if err := f.checkSubmittedKeys(keys); err != nil {
		return nil, err
	}
	out := &FormSubmission{Attributes: RawAttributes{}}
	for _, fd := range f.ActiveFields(defs) {
		raw, present := values[fd.Key]
		if present && isEmptyValue(raw) {
			present = false
		}
		if !present {
			if fd.Required {
				return nil, invalidSubmission("%s es obligatorio", fd.Label)
			}
			continue
		}
		switch fd.Key {
		case FieldEmail, FieldFirstName, FieldLastName:
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return nil, invalidSubmission("%s no es valido", fd.Label)
			}
			if err := out.setBuiltin(fd, s); err != nil {
				return nil, err
			}
		default:
			if _, err := ParseAttributeValue(defs[fd.Key].Type, raw); err != nil {
				return nil, invalidSubmission("%s no es valido", fd.Label)
			}
			out.Attributes[fd.Key] = raw
		}
	}
	return out, nil
}

func (s *FormSubmission) setBuiltin(fd FormField, raw string) error {
	switch fd.Key {
	case FieldEmail:
		email, err := NormalizeEmail(raw)
		if err != nil {
			return invalidSubmission("%s no es una direccion de correo valida", fd.Label)
		}
		s.Email = email
	case FieldFirstName, FieldLastName:
		name, err := NormalizeName(raw)
		if err != nil {
			return invalidSubmission("%s admite como maximo %d caracteres", fd.Label, MaxNameLength)
		}
		if fd.Key == FieldFirstName {
			s.FirstName = name
		} else {
			s.LastName = name
		}
	}
	return nil
}

func isEmptyValue(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "null" || s == `""`
}

// FormValuesFromStrings convierte los valores de un formulario HTML (todo texto) al JSON de
// cada tipo: un numero va como literal, una casilla como booleano (marcada o no) y el resto
// como cadena. Lo que no se puede convertir se rechaza con la etiqueta del campo. Las casillas
// sin marcar no llegan en el cuerpo: quedan en false si el campo es booleano.
func (f *SubscriptionForm) FormValuesFromStrings(in map[string]string, defs Definitions) (map[string]json.RawMessage, error) {
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	if err := f.checkSubmittedKeys(keys); err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(in))
	for _, fd := range f.ActiveFields(defs) {
		typ, _ := FieldType(fd.Key, defs)
		v, present := in[fd.Key]
		if typ == string(AttrBoolean) {
			out[fd.Key] = json.RawMessage(strconv.FormatBool(present && checkboxOn(v)))
			continue
		}
		if !present {
			continue
		}
		v = strings.TrimSpace(v)
		if v == "" {
			out[fd.Key] = json.RawMessage(`""`)
			continue
		}
		if typ == string(AttrNumber) {
			// El literal pasa tal cual (sin float64 de por medio, que perderia precision); el tipo
			// y el rango los comprueba ParseAttributeValue.
			if !jsonNumberRegex.MatchString(v) || len(v) > maxNumberLength {
				return nil, invalidSubmission("%s tiene que ser un numero", fd.Label)
			}
			out[fd.Key] = json.RawMessage(v)
			continue
		}
		b, err := json.Marshal(v)
		if err != nil {
			return nil, invalidSubmission("%s no es valido", fd.Label)
		}
		out[fd.Key] = b
	}
	return out, nil
}

var jsonNumberRegex = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

func checkboxOn(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "1", "yes", "si":
		return true
	}
	return false
}

// SubmissionOutcome es lo que hizo un envio valido. La respuesta publica es la misma en todos:
// quien envia no aprende si la direccion ya existia ni si esta excluida.
type SubmissionOutcome string

const (
	OutcomeConfirmationSent  SubmissionOutcome = "confirmation_sent"
	OutcomeAlreadySubscribed SubmissionOutcome = "already_subscribed"
	OutcomeNotReachable      SubmissionOutcome = "not_reachable"
)

// SubmissionOutcomeFor decide que hacer con el contacto (ya bloqueado) de un envio: a quien
// ya consiente no se le vuelve a pedir, a una direccion excluida de todo envio (rebote, queja,
// no valida, exclusion manual) no se le envia la confirmacion, y a los demas se les pide el
// doble opt-in, tambien a quien se dio de baja: la confirmacion es la unica prueba que lo
// devuelve (CheckConfirmationRequest).
func SubmissionOutcomeFor(c *Contact) SubmissionOutcome {
	switch {
	case c.Status.BlocksAllMail():
		return OutcomeNotReachable
	case c.ConsentStatus == ConsentGranted:
		return OutcomeAlreadySubscribed
	}
	return OutcomeConfirmationSent
}

// FormSubmissionRecord es la fila de estadisticas de un envio valido.
type FormSubmissionRecord struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	FormID    uuid.UUID
	ContactID uuid.UUID
	TokenID   *uuid.UUID
	ListID    uuid.UUID
	Outcome   SubmissionOutcome
}

// FormStats son los envios y confirmaciones de un formulario en un periodo.
type FormStats struct {
	From              time.Time      `json:"from"`
	To                time.Time      `json:"to"`
	Submitted         int64          `json:"submitted"`
	ConfirmationSent  int64          `json:"confirmation_sent"`
	AlreadySubscribed int64          `json:"already_subscribed"`
	NotReachable      int64          `json:"not_reachable"`
	Confirmed         int64          `json:"confirmed"`
	Daily             []FormStatsDay `json:"daily"`
}

// FormStatsDay es un dia (UTC) de la serie.
type FormStatsDay struct {
	Date      string `json:"date"`
	Submitted int64  `json:"submitted"`
	Confirmed int64  `json:"confirmed"`
}

// FormConsentSource es el origen del consentimiento pendiente que pide un formulario.
func FormConsentSource(formID uuid.UUID) string { return formSubmissionSourcePrefix + formID.String() }

// FormConsentEvidence es la evidencia del consentimiento pedido por un formulario: que
// formulario, el texto exacto aceptado con su huella, la ip truncada y el origen del navegador.
// Nunca la ip completa: basta para situar el envio sin identificar a la persona.
func FormConsentEvidence(f *SubscriptionForm, ip, origin string) map[string]any {
	sum := sha256.Sum256([]byte(f.Texts.ConsentText))
	ev := map[string]any{
		"form_id":             f.ID.String(),
		"form_name":           f.Name,
		"consent_text":        f.Texts.ConsentText,
		"consent_text_sha256": hex.EncodeToString(sum[:]),
	}
	if p := TruncateIP(ip); p != "" {
		ev["ip_prefix"] = p
	}
	if origin != "" {
		ev["origin"] = truncate(origin, maxFormOriginLength)
	}
	return ev
}

// TruncateIP reduce una ip a su red: /24 en IPv4 y /48 en IPv6. Vacio si no es una ip.
func TruncateIP(raw string) string {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.Mask(net.CIDRMask(24, 32)).String() + "/24"
	}
	return ip.Mask(net.CIDRMask(48, 128)).String() + "/48"
}
