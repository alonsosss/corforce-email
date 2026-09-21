package domain

import (
	"net/netip"
	"strings"
	"unicode/utf8"
)

// TLSMode es como el ejecutor abre la conexion con el servidor de origen.
type TLSMode string

const (
	TLSImplicit TLSMode = "ssl"
	TLSStartTLS TLSMode = "starttls"
	// TLSNone solo existe si el operador lo permite expresamente (SourcePolicy.AllowPlaintext):
	// la contrasena y el correo viajarian en claro.
	TLSNone TLSMode = "none"
)

const (
	maxHostLen     = 253
	maxLabelLen    = 63
	maxUsernameLen = 320
	maxPasswordLen = 1024
)

// SourcePolicy son las reglas de operador sobre el servidor de origen.
type SourcePolicy struct {
	Ports          []int
	AllowPlaintext bool
	// AllowPrivate admite servidores de origen en direcciones internas. Solo lo activa el entorno
	// de pruebas (main lo rechaza fuera de desarrollo y prueba).
	AllowPrivate bool
}

// TLSModes lista los modos que la politica ofrece.
func (p SourcePolicy) TLSModes() []TLSMode {
	modes := []TLSMode{TLSImplicit, TLSStartTLS}
	if p.AllowPlaintext {
		modes = append(modes, TLSNone)
	}
	return modes
}

// DefaultTLS es el modo que corresponde a un puerto IMAP: 993 es TLS implicito y el resto arranca en
// claro y sube con STARTTLS.
func DefaultTLS(port int) TLSMode {
	if port == 993 {
		return TLSImplicit
	}
	return TLSStartTLS
}

// Source es el servidor y la cuenta de origen tal como los da el usuario.
type Source struct {
	Host     string
	Port     int
	TLS      TLSMode
	Username string
	Password string
}

// Normalize valida y normaliza el origen en su sitio. No resuelve nombres: la comprobacion de que
// el servidor es publico la completa la aplicacion con el DNS (CheckAddresses).
func (p SourcePolicy) Normalize(s *Source) error {
	host, err := normalizeHost(s.Host)
	if err != nil {
		return fieldErr("source_host", err)
	}
	s.Host = host
	if addr, err := netip.ParseAddr(host); err == nil && !p.AllowPrivate && !IsPublicAddr(addr) {
		return fieldErr("source_host", ErrHostNotAllowed)
	}
	if !p.portAllowed(s.Port) {
		return fieldErr("source_port", ErrInvalidPort)
	}
	switch s.TLS {
	case TLSImplicit, TLSStartTLS:
	case TLSNone:
		if !p.AllowPlaintext {
			return fieldErr("source_tls", ErrInvalidTLS)
		}
	default:
		return fieldErr("source_tls", ErrInvalidTLS)
	}
	s.Username = strings.TrimSpace(s.Username)
	if s.Username == "" || utf8.RuneCountInString(s.Username) > maxUsernameLen || hasControl(s.Username) {
		return fieldErr("source_username", ErrInvalidUsername)
	}
	if s.Password == "" || len(s.Password) > maxPasswordLen || !utf8.ValidString(s.Password) || hasLineBreakOrNUL(s.Password) {
		return fieldErr("source_password", ErrInvalidPassword)
	}
	return nil
}

// CheckAddresses aplica la regla anti-SSRF a lo que resolvio el DNS: todas las direcciones deben ser
// publicas, porque el ejecutor puede terminar conectando a cualquiera de ellas.
func (p SourcePolicy) CheckAddresses(addrs []netip.Addr) error {
	if len(addrs) == 0 {
		return fieldErr("source_host", ErrHostUnresolvable)
	}
	if p.AllowPrivate {
		return nil
	}
	for _, a := range addrs {
		if !IsPublicAddr(a) {
			return fieldErr("source_host", ErrHostNotAllowed)
		}
	}
	return nil
}

func (p SourcePolicy) portAllowed(port int) bool {
	for _, allowed := range p.Ports {
		if port == allowed {
			return true
		}
	}
	return false
}

// normalizeHost admite un nombre DNS en ASCII (los internacionales, en punycode) con al menos un
// punto y una ultima etiqueta que no sea solo numerica, o una direccion IP literal. Lo demas se
// rechaza sin resolverlo: un nombre de una sola etiqueta se completaria con los dominios de busqueda
// del sistema y uno numerico se leeria como una IP en formas que el DNS no ve ("2130706433").
func normalizeHost(raw string) (string, error) {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if host == "" || len(host) > maxHostLen {
		return "", ErrInvalidHost
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Zone() != "" {
			return "", ErrInvalidHost
		}
		return addr.Unmap().String(), nil
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return "", ErrInvalidHost
	}
	for _, l := range labels {
		if l == "" || len(l) > maxLabelLen || l[0] == '-' || l[len(l)-1] == '-' {
			return "", ErrInvalidHost
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", ErrInvalidHost
			}
		}
	}
	if allDigits(labels[len(labels)-1]) {
		return "", ErrInvalidHost
	}
	return host, nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// hasLineBreakOrNUL rechaza lo que el fichero de contrasena del ejecutor no puede llevar: su
// primera linea es la contrasena, y un salto de linea la cortaria.
func hasLineBreakOrNUL(s string) bool {
	return strings.ContainsAny(s, "\r\n\x00")
}
