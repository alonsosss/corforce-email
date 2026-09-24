package domain

import (
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Errores de la baja de un boletin.
var (
	ErrUnsubscribeNotAvailable = errors.New("el mensaje no ofrece una baja que el webmail pueda hacer")
	ErrUnsubscribeRefused      = errors.New("la dirección de baja no es una URL pública admitida")
	ErrUnsubscribeFailed       = errors.New("el remitente no confirmó la baja")
)

// UnsubscribeMethod es como se hace la baja, por orden de preferencia.
type UnsubscribeMethod string

const (
	// UnsubscribeOneClick es el POST de RFC 8058 que hace el propio servicio.
	UnsubscribeOneClick UnsubscribeMethod = "one_click"
	// UnsubscribeMailto es un correo desde el buzon a la direccion de baja (RFC 2369).
	UnsubscribeMailto UnsubscribeMethod = "mailto"
	// UnsubscribeWeb es una pagina que el usuario abre por su cuenta: el servicio no la visita.
	UnsubscribeWeb UnsubscribeMethod = "web"
)

// Topes de la baja. Un List-Unsubscribe con mas URIs que estas es anomalo: se leen las primeras.
const (
	maxUnsubscribeURIs      = 5
	MaxUnsubscribeURLBytes  = 2048
	maxMailtoSubjectRunes   = 200
	maxMailtoBodyRunes      = 1000
	defaultMailtoSubject    = "unsubscribe"
	oneClickPostValue       = "list-unsubscribe=one-click"
	unsubscribeHTTPSPort    = "443"
	unsubscribeSchemeHTTPS  = "https"
	unsubscribeSchemeMailto = "mailto"
)

// MailtoTarget es el correo de baja: una sola direccion, un asunto y un cuerpo acotados. Cc, Bcc y
// cualquier otra cabecera del URI se ignoran.
type MailtoTarget struct {
	Address Address
	Subject string
	Body    string
}

// Unsubscribe es la baja que ofrece un mensaje. Method vacio: no ofrece ninguna utilizable.
type Unsubscribe struct {
	Method UnsubscribeMethod
	// URL es la de one_click o web (https); Mailto la de mailto.
	URL    string
	Mailto *MailtoTarget
}

// UnsubscribeAllowedIn dice si desde una carpeta se ofrece la baja. En lo que escribe el propio
// buzon no hay boletin, y desde Spam darse de baja confirma al remitente que la direccion existe.
// Tambien impide que el buzon use la baja para que el servicio llame a una URL que el mismo puso en
// un borrador.
func UnsubscribeAllowedIn(role FolderRole) bool {
	switch role {
	case RoleSent, RoleDrafts, RoleScheduled, RoleJunk:
		return false
	}
	return true
}

// Host es el servidor de la URL de baja, lo unico que se registra de ella: la ruta suele llevar un
// token del destinatario.
func (u Unsubscribe) Host() string {
	if parsed, err := url.Parse(u.URL); err == nil {
		return parsed.Hostname()
	}
	return ""
}

// ParseUnsubscribe lee List-Unsubscribe y List-Unsubscribe-Post (RFC 2369 y RFC 8058). La baja en
// un clic solo se ofrece con el POST declarado y una URL https; sin el, la URL https queda como
// pagina que el usuario abre y, si hay mailto, se prefiere el correo.
func ParseUnsubscribe(h MessageHeaders) Unsubscribe {
	var httpsURL string
	var mailto *MailtoTarget
	for _, raw := range unsubscribeURIs(h.First(HeaderListUnsubscribe)) {
		switch {
		case httpsURL == "" && strings.HasPrefix(strings.ToLower(raw), unsubscribeSchemeHTTPS+":"):
			if u, err := ValidateUnsubscribeURL(raw); err == nil {
				httpsURL = u.String()
			}
		case mailto == nil && strings.HasPrefix(strings.ToLower(raw), unsubscribeSchemeMailto+":"):
			mailto = parseMailto(raw)
		}
	}
	oneClick := false
	for _, v := range h[HeaderListUnsubscribePost] {
		if strings.EqualFold(strings.TrimSpace(v), oneClickPostValue) {
			oneClick = true
		}
	}
	switch {
	case httpsURL != "" && oneClick:
		return Unsubscribe{Method: UnsubscribeOneClick, URL: httpsURL}
	case mailto != nil:
		return Unsubscribe{Method: UnsubscribeMailto, Mailto: mailto}
	case httpsURL != "":
		return Unsubscribe{Method: UnsubscribeWeb, URL: httpsURL}
	}
	return Unsubscribe{}
}

// unsubscribeURIs separa la lista "<uri>, <uri>" de List-Unsubscribe.
func unsubscribeURIs(header string) []string {
	var out []string
	for len(out) < maxUnsubscribeURIs {
		start := strings.IndexByte(header, '<')
		if start < 0 {
			break
		}
		end := strings.IndexByte(header[start:], '>')
		if end < 0 {
			break
		}
		if uri := strings.TrimSpace(header[start+1 : start+end]); uri != "" {
			out = append(out, uri)
		}
		header = header[start+end+1:]
	}
	return out
}

// ValidateUnsubscribeURL admite solo https al puerto por defecto, sin credenciales ni fragmento y
// con un host. Que el host resuelva a una direccion publica lo comprueba quien conecta, en cada
// conexion.
func ValidateUnsubscribeURL(raw string) (*url.URL, error) {
	if len(raw) > MaxUnsubscribeURLBytes || strings.ContainsAny(raw, " \t\r\n\x00") {
		return nil, ErrUnsubscribeRefused
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, unsubscribeSchemeHTTPS) || u.User != nil || u.Opaque != "" || u.Hostname() == "" {
		return nil, ErrUnsubscribeRefused
	}
	if p := u.Port(); p != "" && p != unsubscribeHTTPSPort {
		return nil, ErrUnsubscribeRefused
	}
	u.Scheme = unsubscribeSchemeHTTPS
	u.Fragment = ""
	return u, nil
}

func parseMailto(raw string) *MailtoTarget {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque == "" {
		return nil
	}
	to, err := url.PathUnescape(u.Opaque)
	if err != nil || strings.Contains(to, ",") {
		return nil
	}
	addr, err := NewAddress("mailto", "", to)
	if err != nil {
		return nil
	}
	q := u.Query()
	subject := cleanMailtoText(q.Get("subject"), maxMailtoSubjectRunes, true)
	if subject == "" {
		subject = defaultMailtoSubject
	}
	return &MailtoTarget{
		Address: addr,
		Subject: subject,
		Body:    cleanMailtoText(q.Get("body"), maxMailtoBodyRunes, false),
	}
}

// cleanMailtoText acota un texto que dicta el remitente del boletin y quita lo que no puede ir en
// el correo: controles y, en el asunto, saltos de linea.
func cleanMailtoText(v string, maxRunes int, singleLine bool) string {
	if !utf8.ValidString(v) {
		return ""
	}
	var b strings.Builder
	n := 0
	for _, r := range v {
		if n == maxRunes {
			break
		}
		if r == '\n' && !singleLine {
			b.WriteRune(r)
			n++
			continue
		}
		if isControl(r) || isBidiControl(r) {
			continue
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}
