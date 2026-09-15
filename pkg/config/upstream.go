package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"unicode"
)

// Direcciones internas de otros servicios.
//
// Quien llama a otro servicio por la red interna lo localiza con <HOST_ENV> (host) y
// <HOST_ENV>_PORT (puerto), con los de compose por defecto: el gateway, cada servicio de su tabla
// de rutas; organization, los que llama su saga. Se resuelven al arrancar: un puerto fuera de
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
