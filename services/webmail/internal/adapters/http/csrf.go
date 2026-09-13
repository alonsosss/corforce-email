package http

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/response"
)

// OriginGuard es la segunda barrera contra CSRF, detras de SameSite=Strict: toda
// peticion que escribe debe traer un Origin de la lista (CORS_ALLOWED_ORIGINS y
// API_ORIGIN). Sin Origin se rechaza: los navegadores lo envian en toda peticion que no
// es GET, y un cliente que no lo envia no es la aplicacion web.
type OriginGuard struct {
	allowed map[string]struct{}
}

func NewOriginGuard(origins []string) (*OriginGuard, error) {
	g := &OriginGuard{allowed: map[string]struct{}{}}
	for _, o := range origins {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		n, err := normalizeOrigin(o)
		if err != nil {
			return nil, fmt.Errorf("webmail: origen permitido invalido %q: %w", o, err)
		}
		g.allowed[n] = struct{}{}
	}
	if len(g.allowed) == 0 {
		return nil, errors.New("webmail: no hay origenes permitidos (CORS_ALLOWED_ORIGINS o API_ORIGIN)")
	}
	return g, nil
}

// normalizeOrigin deja esquema://host[:puerto] en minusculas y sin el puerto por defecto,
// que el navegador omite en la cabecera Origin.
func normalizeOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("se esperaba esquema://host[:puerto]")
	}
	host := strings.ToLower(u.Host)
	switch {
	case u.Scheme == "https" && strings.HasSuffix(host, ":443"):
		host = strings.TrimSuffix(host, ":443")
	case u.Scheme == "http" && strings.HasSuffix(host, ":80"):
		host = strings.TrimSuffix(host, ":80")
	}
	return u.Scheme + "://" + host, nil
}

// Allowed indica si el Origin recibido esta en la lista.
func (g *OriginGuard) Allowed(origin string) bool {
	if origin == "" || origin == "null" {
		return false
	}
	n, err := normalizeOrigin(origin)
	if err != nil {
		return false
	}
	_, ok := g.allowed[n]
	return ok
}

func (g *OriginGuard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if !g.Allowed(r.Header.Get("Origin")) {
			response.Err(w, http.StatusForbidden, "ORIGIN_NOT_ALLOWED", "origen no permitido")
			return
		}
		next.ServeHTTP(w, r)
	})
}
