package oauth2

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ValidateRedirectURI aplica las reglas de validacion que Google exige a los
// clientes web. Se comprueba en el arranque y antes de construir cada URL de
// autorizacion: un redirect mal formado no da un error claro en el proveedor,
// da una pantalla generica de "redirect_uri_mismatch" que cuesta horas.
//
// La plataforma es mas estricta que el proveedor en un punto: no se admite query. El
// redirect es una ruta fija del despliegue y una query solo abre discusion
// sobre redirecciones abiertas.
func ValidateRedirectURI(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("oauth2: falta el redirect URI")
	}
	if strings.Contains(raw, "*") {
		return fmt.Errorf("oauth2: el redirect URI no admite comodines")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("oauth2: redirect URI invalido: %w", err)
	}
	if parsed.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("oauth2: el redirect URI no admite fragmento")
	}
	if parsed.RawQuery != "" {
		return fmt.Errorf("oauth2: el redirect URI no admite query")
	}
	if parsed.User != nil {
		return fmt.Errorf("oauth2: el redirect URI no admite credenciales en la URL")
	}
	if strings.Contains(parsed.Path, "/..") {
		return fmt.Errorf("oauth2: el redirect URI no admite recorrido de rutas")
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("oauth2: el redirect URI necesita host")
	}
	loopback := isLoopback(host)
	switch parsed.Scheme {
	case "https":
	case "http":
		if !loopback {
			return fmt.Errorf("oauth2: el redirect URI debe usar https (solo localhost admite http)")
		}
	default:
		return fmt.Errorf("oauth2: esquema no admitido en el redirect URI: %s", parsed.Scheme)
	}
	if !loopback && net.ParseIP(host) != nil {
		return fmt.Errorf("oauth2: el redirect URI no admite direcciones IP")
	}
	if !loopback && !strings.Contains(host, ".") {
		return fmt.Errorf("oauth2: el redirect URI necesita un dominio completo")
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
