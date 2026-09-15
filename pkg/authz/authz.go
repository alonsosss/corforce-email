// Package authz es la tercera capa de control de acceso: el handler exige el permiso
// concreto (module, resource, action) ademas del gateo por modulo que ya hizo el gateway.
//
// La politica del usuario la tiene access-control; aqui se consulta por HTTP con el token
// interno y se guarda en memoria un minuto por usuario y empresa, el mismo horizonte que
// usa el gateway. Si access-control no responde y no hay politica en cache, se DENIEGA:
// un permiso que no se puede comprobar no se concede.
package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
)

const cacheTTL = time.Minute

type permission struct{ Module, Resource, Action string }

type policy struct {
	perms   []permission
	expires time.Time
}

// Checker resuelve permisos contra access-control.
type Checker struct {
	accessURL string
	token     string
	client    *httpclient.Client

	mu    sync.Mutex
	cache map[string]policy
}

// NewChecker construye el comprobador. accessURL es la URL interna de access-control
// (p. ej. http://access-control:8002) y token el INTERNAL_GATEWAY_TOKEN con el que ese
// servicio acepta llamadas entre servicios.
func NewChecker(accessURL, token string) *Checker {
	return &Checker{
		accessURL: accessURL,
		token:     token,
		client:    httpclient.New("access-control", httpclient.Options{Timeout: 3 * time.Second, MaxAttempts: 2}),
		cache:     make(map[string]policy),
	}
}

const (
	accessControlURLEnv     = "ACCESS_CONTROL_URL"
	defaultAccessControlURL = "http://access-control:8002"
)

// CheckerFromEnv lee ACCESS_CONTROL_URL (config.ServiceURL, por defecto
// http://access-control:8002) e INTERNAL_GATEWAY_TOKEN. Una URL mal formada es un error de
// arranque: con ella cada permiso se denegaria en la primera peticion.
func CheckerFromEnv() (*Checker, error) {
	url, err := config.ServiceURL(accessControlURLEnv, defaultAccessControlURL)
	if err != nil {
		return nil, err
	}
	return NewChecker(url, os.Getenv("INTERNAL_GATEWAY_TOKEN")), nil
}

// NewCheckerFromEnv es CheckerFromEnv sin validar ACCESS_CONTROL_URL.
//
// Deprecated: usa CheckerFromEnv. Queda solo para mail-security y domain-service hasta que
// lean su configuracion con la regla de config.ServiceURL.
func NewCheckerFromEnv() *Checker {
	url := os.Getenv(accessControlURLEnv)
	if url == "" {
		url = defaultAccessControlURL
	}
	return NewChecker(url, os.Getenv("INTERNAL_GATEWAY_TOKEN"))
}

// Allowed indica si el usuario de la peticion puede (module, resource, action). El
// superadmin y el tenant_admin pasan sin consultar: son los roles del sistema y sus
// permisos no se editan.
func (c *Checker) Allowed(ctx context.Context, module, resource, action string) (bool, error) {
	if middleware.IsPrivileged(ctx) {
		return true, nil
	}
	userID, tenantID := middleware.GetUserID(ctx), middleware.GetTenantID(ctx)
	if userID == "" || tenantID == "" {
		return false, nil
	}
	pol, err := c.policyFor(ctx, userID, tenantID)
	if err != nil {
		return false, err
	}
	for _, p := range pol.perms {
		if p.Module != module {
			continue
		}
		if (p.Resource == resource || p.Resource == "*") && (p.Action == action || p.Action == "*") {
			return true, nil
		}
	}
	return false, nil
}

func (c *Checker) policyFor(ctx context.Context, userID, tenantID string) (policy, error) {
	key := userID + ":" + tenantID
	c.mu.Lock()
	cached, ok := c.cache[key]
	c.mu.Unlock()
	if ok && time.Now().Before(cached.expires) {
		return cached, nil
	}
	pol, err := c.fetch(ctx, userID, tenantID)
	if err != nil {
		if ok {
			return cached, nil
		}
		return policy{}, err
	}
	c.mu.Lock()
	c.cache[key] = pol
	c.mu.Unlock()
	return pol, nil
}

func (c *Checker) fetch(ctx context.Context, userID, tenantID string) (policy, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.accessURL+"/api/v1/policy/"+userID, nil)
	if err != nil {
		return policy{}, err
	}
	req.Header.Set("X-Gateway-Token", c.token)
	req.Header.Set("X-User-ID", userID)
	req.Header.Set("X-Tenant-ID", tenantID)
	resp, err := c.client.Do(req)
	if err != nil {
		return policy{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return policy{}, fmt.Errorf("access-control policy: status %d", resp.StatusCode)
	}
	var payload struct {
		Data struct {
			Permissions []struct {
				Module   string `json:"module"`
				Resource string `json:"resource"`
				Action   string `json:"action"`
			} `json:"permissions"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return policy{}, err
	}
	pol := policy{expires: time.Now().Add(cacheTTL)}
	for _, p := range payload.Data.Permissions {
		pol.perms = append(pol.perms, permission{p.Module, p.Resource, p.Action})
	}
	return pol, nil
}

// Invalidate olvida la politica en cache de un usuario (tras asignar o revocar un rol).
func (c *Checker) Invalidate(userID, tenantID string) {
	c.mu.Lock()
	delete(c.cache, userID+":"+tenantID)
	c.mu.Unlock()
}

// RequirePermission es el middleware de handler: 403 FORBIDDEN si el usuario no tiene el
// permiso, 503 si no se pudo comprobar y no habia politica conocida.
func (c *Checker) RequirePermission(module, resource, action string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, err := c.Allowed(r.Context(), module, resource, action)
			if err != nil {
				response.Err(w, http.StatusServiceUnavailable, "AUTHZ_UNAVAILABLE", "no se pudo comprobar el permiso")
				return
			}
			if !ok {
				response.ErrForbidden(w, "su rol no tiene permiso para esta operacion")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
