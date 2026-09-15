package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode"
)

// Direcciones internas de otros servicios.
//
// Quien llama a otro servicio por la red interna lo localiza con <HOST_ENV> (host) y
// <HOST_ENV>_PORT (puerto), con los de compose por defecto: el gateway, cada servicio de su tabla
// de rutas; organization, los que llama su saga. Los demas lo localizan con una URL base,
// <SERVICIO>_URL, que se lee con ServiceURL. Se resuelven al arrancar: un puerto fuera de
// rango o un host que no cabe en una URL impiden arrancar, en vez de dar 502 en la primera
// peticion que los use.

// UpstreamURL devuelve la URL http interna de un servicio: el host de hostEnv y el puerto de
// hostEnv+"_PORT" (EnvInt entre 1 y MaxPort), o defaultHost y defaultPort si faltan. Como en
// EnvInt, los valores por defecto se validan en cada llamada, esten o no las variables.
func UpstreamURL(hostEnv, defaultHost string, defaultPort int) (string, error) {
	if !ValidHost(defaultHost) {
		return "", fmt.Errorf("%s: default host %q is not a host name or IP address", hostEnv, defaultHost)
	}
	host := strings.TrimSpace(os.Getenv(hostEnv))
	if host == "" {
		host = defaultHost
	} else if !ValidHost(host) {
		return "", fmt.Errorf("%s=%q must be a host name or an IP address (IPv6 without brackets)", hostEnv, host)
	}
	port, err := EnvInt(hostEnv+"_PORT", defaultPort, 1, MaxPort)
	if err != nil {
		return "", err
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port)), nil
}

// errServiceURL es la regla de una URL base interna: quien la usa le pega rutas absolutas
// ("/internal/..."), asi que no lleva ruta propia, y nada de lo que viaja con ella (usuario,
// consulta, fragmento) tiene destino en una llamada entre servicios.
var errServiceURL = errors.New("must be an http or https base URL, scheme://host[:port], without user info, path, query or fragment")

// ServiceURL lee de key la URL base interna de otro servicio (ORGANIZATION_URL,
// SUPPRESSION_URL...) y la devuelve como scheme://host[:port], sin barra final. Admite http o
// https, un host valido para ValidHost (IPv6 entre corchetes), un puerto opcional entre 1 y
// MaxPort y como mucho la barra de la raiz; ningun usuario, ruta, consulta ni fragmento.
// Ausente o en blanco vale def, que puede ser "" en una variable opcional. Como en EnvInt, un
// def no vacio se valida en cada llamada.
func ServiceURL(key, def string) (string, error) {
	base := ""
	if def != "" {
		var err error
		if base, err = serviceBaseURL(def); err != nil {
			return "", fmt.Errorf("%s: default %q: %w", key, def, err)
		}
	}
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return base, nil
	}
	u, err := serviceBaseURL(raw)
	if err != nil {
		return "", fmt.Errorf("%s=%q: %w", key, raw, err)
	}
	return u, nil
}

// RequiredServiceURL es ServiceURL para una variable sin valor por defecto: ausente o en
// blanco es un error.
func RequiredServiceURL(key string) (string, error) {
	u, err := ServiceURL(key, "")
	if err == nil && u == "" {
		return "", fmt.Errorf("%s is required: %w", key, errServiceURL)
	}
	return u, err
}

func serviceBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawPath != "" || strings.ContainsAny(raw, "?#") {
		return "", errServiceURL
	}
	host := u.Hostname()
	// Sin corchetes, url.Parse toma el ultimo grupo de una IPv6 por puerto.
	if !ValidHost(host) || (strings.Contains(host, ":") && !strings.HasPrefix(u.Host, "[")) {
		return "", errServiceURL
	}
	if port := u.Port(); port != "" {
		if _, err := ParsePort(port); err != nil {
			return "", err
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", errServiceURL
	}
	return u.Scheme + "://" + u.Host, nil
}

// ParsePort interpreta un puerto TCP decimal entre 1 y MaxPort.
func ParsePort(raw string) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > MaxPort {
		return 0, fmt.Errorf("%q must be a port between 1 and %d", raw, MaxPort)
	}
	return n, nil
}

// ValidHost dice si host puede ir como host de una URL http interna: no vacio, sin espacios,
// caracteres de control ni delimitadores de URL, y con dos puntos solo si es una direccion IPv6,
// que va sin corchetes (los pone quien arma la URL). No resuelve nombres.
func ValidHost(host string) bool {
	if host == "" || strings.ContainsAny(host, `/\?#@[]%`) ||
		strings.IndexFunc(host, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return false
	}
	return !strings.Contains(host, ":") || net.ParseIP(host) != nil
}
