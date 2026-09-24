package domain

import (
	"strings"
)

// Category es la pestana de la bandeja inteligente en la que cae un mensaje. Se calcula con
// reglas deterministas sobre las cabeceras: nada sale del servicio ni se aprende.
type Category string

const (
	CategoryPrimary       Category = "primary"
	CategoryNotifications Category = "notifications"
	CategoryNewsletters   Category = "newsletters"
)

// Categories son las pestanas en el orden en que se muestran.
var Categories = []Category{CategoryPrimary, CategoryNotifications, CategoryNewsletters}

// ParseCategory interpreta el filtro de la query; vacio es sin filtro.
func ParseCategory(raw string) (Category, error) {
	if raw == "" {
		return "", nil
	}
	for _, c := range Categories {
		if raw == string(c) {
			return c, nil
		}
	}
	return "", invalid("category", "debe ser primary, notifications o newsletters")
}

// Cabeceras que deciden la categoria. Las mismas reglas se traducen a IMAP SEARCH para filtrar
// en el servidor (el adaptador IMAP), asi que aqui solo se usan las comparaciones que IMAP sabe
// hacer: presencia de la cabecera y subcadena sin distinguir mayusculas.
const (
	HeaderListID              = "List-Id"
	HeaderListUnsubscribe     = "List-Unsubscribe"
	HeaderListUnsubscribePost = "List-Unsubscribe-Post"
	HeaderPrecedence          = "Precedence"
	HeaderAutoSubmitted       = "Auto-Submitted"
	HeaderAuthResults         = "Authentication-Results"
	HeaderSpamdResult         = "X-Spamd-Result"
)

// AutoSubmittedMarker casa con auto-generated, auto-replied y auto-notified (RFC 3834) y no con
// "no", que es lo que declara un mensaje escrito por una persona.
const AutoSubmittedMarker = "auto-"

// NewsletterPrecedence y NotificationPrecedences son los valores de Precedence que cuentan.
const NewsletterPrecedence = "list"

var NotificationPrecedences = []string{"bulk", "junk"}

// NotificationSenderMarkers son subcadenas del remitente (nombre y direccion) de los envios
// automaticos que no esperan respuesta.
var NotificationSenderMarkers = []string{
	"noreply", "no-reply", "no_reply", "donotreply", "do-not-reply", "do_not_reply",
	"mailer-daemon", "postmaster@", "notifications@", "notification@", "alerts@",
}

// MessageHeaders son las cabeceras de un mensaje que usan la bandeja inteligente, el escudo y la
// baja: cada clave (en su forma canonica, como las constantes Header*) con todos sus valores, ya
// desplegados en una linea.
type MessageHeaders map[string][]string

// Has dice si la cabecera esta presente, aunque vacia.
func (h MessageHeaders) Has(key string) bool { return len(h[key]) > 0 }

// First es el primer valor de la cabecera, vacio si no esta.
func (h MessageHeaders) First(key string) string {
	if v := h[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// Contains dice si algun valor de la cabecera contiene sub sin distinguir mayusculas.
func (h MessageHeaders) Contains(key, sub string) bool {
	sub = strings.ToLower(sub)
	for _, v := range h[key] {
		if strings.Contains(strings.ToLower(v), sub) {
			return true
		}
	}
	return false
}

// IsNewsletter: ofrece baja o viene de una lista.
func (h MessageHeaders) IsNewsletter() bool {
	return h.Has(HeaderListUnsubscribe) || h.Has(HeaderListID) || h.Contains(HeaderPrecedence, NewsletterPrecedence)
}

// Classify decide la pestana. En orden: un mensaje generado por una maquina (Auto-Submitted) es
// una notificacion aunque lleve baja; uno con baja o de una lista es un boletin; uno masivo o de un
// remitente que no admite respuesta es una notificacion; el resto es principal.
func Classify(h MessageHeaders, from []Address) Category {
	if h.Contains(HeaderAutoSubmitted, AutoSubmittedMarker) {
		return CategoryNotifications
	}
	if h.IsNewsletter() {
		return CategoryNewsletters
	}
	for _, p := range NotificationPrecedences {
		if h.Contains(HeaderPrecedence, p) {
			return CategoryNotifications
		}
	}
	for _, a := range from {
		who := strings.ToLower(a.Name + " <" + a.Email + ">")
		for _, m := range NotificationSenderMarkers {
			if strings.Contains(who, m) {
				return CategoryNotifications
			}
		}
	}
	return CategoryPrimary
}
