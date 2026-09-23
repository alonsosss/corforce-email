package domain

import (
	"crypto/sha256"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// MaxLinkLen acota la URL normalizada que se agrega; una mas larga no se desglosa.
	MaxLinkLen = 2048
	// MaxLinksPerCampaign acota las URL distintas de una campana. Un enlace personalizado
	// por destinatario que la normalizacion no reconoce abriria una fila por persona; a
	// partir del tope los clics nuevos se suman en OtherLinks.
	MaxLinksPerCampaign = 500
	// OtherLinks es la fila que acumula los clics de las URL por encima del tope.
	OtherLinks = ""

	// DefaultLinksLimit y MaxLinksLimit son el tamano por defecto y el maximo de la lista
	// de enlaces de una campana.
	DefaultLinksLimit = 100
	MaxLinksLimit     = MaxLinksPerCampaign + 1
)

// redacted sustituye en la ruta un identificador personal del destinatario.
const redacted = "-"

// NormalizeLink devuelve la URL de un clic lista para agregarse sin datos personales, o ""
// si no es un enlace http(s) agregable. Quita los parametros utm_ (los anade el envio),
// los que llevan un identificador del destinatario (su direccion, su contacto, su mensaje)
// y las credenciales de la URL; pasa esquema y host a minusculas, quita el puerto por
// defecto y da "/" a la ruta vacia. La web aplica las mismas reglas a los enlaces de la
// vista previa para casarlos (web/src/pages/analytics/linkHeatmap.ts).
func NormalizeLink(raw string, identifiers ...string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.Opaque != "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	ids := make([]string, 0, len(identifiers))
	for _, id := range identifiers {
		if id = strings.ToLower(strings.TrimSpace(id)); len(id) >= 3 {
			ids = append(ids, id)
		}
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" && !(scheme == "http" && port == "80") && !(scheme == "https" && port == "443") {
		host += ":" + port
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if containsAny(decoded(path, url.PathUnescape), ids) {
		segments := strings.Split(path, "/")
		for i, s := range segments {
			if containsAny(decoded(s, url.PathUnescape), ids) {
				segments[i] = redacted
			}
		}
		path = strings.Join(segments, "/")
	}
	var b strings.Builder
	b.WriteString(scheme)
	b.WriteString("://")
	b.WriteString(host)
	b.WriteString(path)
	if query := cleanQuery(u.RawQuery, ids); query != "" {
		b.WriteByte('?')
		b.WriteString(query)
	}
	if u.Fragment != "" && !containsAny(strings.ToLower(u.Fragment), ids) {
		b.WriteByte('#')
		b.WriteString(u.EscapedFragment())
	}
	out := b.String()
	if len(out) > MaxLinkLen {
		return ""
	}
	return out
}

func cleanQuery(raw string, ids []string) string {
	if raw == "" {
		return ""
	}
	kept := make([]string, 0, 4)
	for _, pair := range strings.FieldsFunc(raw, func(r rune) bool { return r == '&' || r == ';' }) {
		key, value, _ := strings.Cut(pair, "=")
		if strings.HasPrefix(strings.ToLower(decoded(key, url.QueryUnescape)), "utm_") {
			continue
		}
		if containsAny(decoded(value, url.QueryUnescape), ids) {
			continue
		}
		kept = append(kept, pair)
	}
	return strings.Join(kept, "&")
}

func decoded(s string, unescape func(string) (string, error)) string {
	if d, err := unescape(s); err == nil {
		s = d
	}
	return strings.ToLower(s)
}

func containsAny(s string, ids []string) bool {
	for _, id := range ids {
		if strings.Contains(s, id) {
			return true
		}
	}
	return false
}

// LinkHash identifica una URL normalizada en las claves de las tablas.
func LinkHash(link string) []byte {
	sum := sha256.Sum256([]byte(link))
	return sum[:]
}

// LinkClick es un clic de un mensaje de campana ya normalizado.
type LinkClick struct {
	TenantID   uuid.UUID
	CampaignID uuid.UUID
	MessageID  uuid.UUID
	URL        string
	At         time.Time
}

// LinkStats son los clics de una URL de una campana. Unique cuenta mensajes distintos:
// en marketing cada mensaje es de una sola persona.
type LinkStats struct {
	URL            string
	Clicks         int64
	UniqueClicks   int64
	FirstClickedAt time.Time
	LastClickedAt  time.Time
}

// CampaignLinks es el desglose de clics por enlace de una campana. TotalLinks y
// TotalClicks cubren todas las URL (tambien OtherLinks), no solo las de la lista.
type CampaignLinks struct {
	CampaignID  uuid.UUID
	TotalLinks  int64
	TotalClicks int64
	Links       []LinkStats
}

// ParseLinksLimit lee el tamano de la lista de enlaces: vacio = el valor por defecto.
func ParseLinksLimit(s string) (int, error) {
	if s == "" {
		return DefaultLinksLimit, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > MaxLinksLimit {
		return 0, &ValidationError{Field: "limit", Message: "debe ser un entero entre 1 y " + strconv.Itoa(MaxLinksLimit)}
	}
	return n, nil
}
