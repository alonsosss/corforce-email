// Package apikey resuelve claves de API de empresa contra access-control para quien las acepta
// (el gateway en las rutas de envio y smtp-relay como credencial SMTP), con una cache corta que
// respeta la revocacion: access-control deja una marca en el Redis de la plataforma al revocar
// y cada uso de una clave en cache la consulta.
//
// El formato del token y el hash los custodia access-control; aqui solo se reconoce la forma
// para no preguntar por lo que no puede ser una clave.
package apikey

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/pkg/middleware"
)

// TokenPrefix abre todo token de clave de API.
const TokenPrefix = "cfm_"

// Limites de la cache: por encima del techo se vacia entera antes de anadir (cada entrada es de
// una clave o de un intento), y un negativo dura poco para que una clave recien creada sirva ya.
const (
	DefaultCacheTTL = 30 * time.Second
	MaxCacheTTL     = 5 * time.Minute
	negativeTTL     = 5 * time.Second
	maxCacheEntries = 10000
	maxTokenLen     = 128
	resolvePath     = "/internal/access-control/api-keys/resolve"
)

var (
	// ErrInvalid: el token no autentica ninguna clave usable.
	ErrInvalid = errors.New("apikey: clave invalida")
	// ErrUnavailable: no se pudo comprobar (access-control caido). Quien llama falla cerrado.
	ErrUnavailable = errors.New("apikey: no se pudo comprobar la clave")
)

// LooksLikeToken dice si un bearer tiene la forma de una clave de API y no de un JWT.
func LooksLikeToken(s string) bool {
	return strings.HasPrefix(s, TokenPrefix) && len(s) <= maxTokenLen
}

// Principal es una clave resuelta: su empresa y su alcance efectivo.
type Principal struct {
	ID        string                   `json:"id"`
	TenantID  string                   `json:"tenant_id"`
	Prefix    string                   `json:"prefix"`
	Scopes    []middleware.APIKeyScope `json:"scopes"`
	ExpiresAt *time.Time               `json:"expires_at,omitempty"`
}

// Allows dice si el alcance incluye el permiso exacto.
func (p *Principal) Allows(module, resource, action string) bool {
	for _, s := range p.Scopes {
		if s.Module == module && s.Resource == resource && s.Action == action {
			return true
		}
	}
	return false
}

// Revocations consulta la marca de revocacion. MarkRevoked la deja quien revoca.
type Revocations interface {
	IsRevoked(ctx context.Context, keyID string) (bool, error)
}

type entry struct {
	principal *Principal
	expires   time.Time
}

// Resolver pregunta a access-control y cachea la respuesta.
type Resolver struct {
	baseURL     string
	token       string
	client      *httpclient.Client
	ttl         time.Duration
	revocations Revocations
	now         func() time.Time

	mu    sync.Mutex
	cache map[[32]byte]entry
}

// NewResolver crea el resolvedor. baseURL es la URL interna de access-control, token el
// INTERNAL_GATEWAY_TOKEN, ttl la vida de la cache (0 = DefaultCacheTTL) y revocations puede ser
// nil (la revocacion llega entonces al vencer la cache).
func NewResolver(baseURL, token string, ttl time.Duration, revocations Revocations) *Resolver {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	if ttl > MaxCacheTTL {
		ttl = MaxCacheTTL
	}
	return &Resolver{
		baseURL:     strings.TrimRight(baseURL, "/"),
		token:       token,
		client:      httpclient.New("access-control", httpclient.Options{Timeout: 3 * time.Second, MaxAttempts: 2}),
		ttl:         ttl,
		revocations: revocations,
		now:         time.Now,
		cache:       make(map[[32]byte]entry),
	}
}

// Resolve devuelve la clave del token, ErrInvalid o ErrUnavailable. clientIP viaja a
// access-control para el ultimo uso.
func (r *Resolver) Resolve(ctx context.Context, token, clientIP string) (*Principal, error) {
	if !LooksLikeToken(token) {
		return nil, ErrInvalid
	}
	sum := sha256.Sum256([]byte(token))
	now := r.now()
	r.mu.Lock()
	cached, ok := r.cache[sum]
	r.mu.Unlock()
	if ok && now.Before(cached.expires) {
		if cached.principal == nil {
			return nil, ErrInvalid
		}
		if r.stillValid(ctx, cached.principal, now) {
			return cached.principal, nil
		}
		r.forget(sum)
		return nil, ErrInvalid
	}

	p, err := r.fetch(ctx, token, clientIP)
	switch {
	case errors.Is(err, ErrInvalid):
		r.store(sum, entry{expires: now.Add(negativeTTL)})
		return nil, ErrInvalid
	case err != nil:
		return nil, err
	}
	expires := now.Add(r.ttl)
	if p.ExpiresAt != nil && p.ExpiresAt.Before(expires) {
		expires = *p.ExpiresAt
	}
	r.store(sum, entry{principal: p, expires: expires})
	return p, nil
}

// stillValid mira la caducidad propia de la clave y la marca de revocacion. Si Redis no
// responde se confia en la cache, que ya es corta.
func (r *Resolver) stillValid(ctx context.Context, p *Principal, now time.Time) bool {
	if p.ExpiresAt != nil && !now.Before(*p.ExpiresAt) {
		return false
	}
	if r.revocations == nil {
		return true
	}
	revoked, err := r.revocations.IsRevoked(ctx, p.ID)
	return err != nil || !revoked
}

// Forget olvida lo cacheado de una clave: quien la usa en una sesion larga (SMTP) la vuelve a
// resolver al ver la marca de revocacion.
func (r *Resolver) Forget(token string) {
	r.forget(sha256.Sum256([]byte(token)))
}

func (r *Resolver) forget(sum [32]byte) {
	r.mu.Lock()
	delete(r.cache, sum)
	r.mu.Unlock()
}

func (r *Resolver) store(sum [32]byte, e entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.cache) >= maxCacheEntries {
		now := r.now()
		for k, v := range r.cache {
			if !now.Before(v.expires) {
				delete(r.cache, k)
			}
		}
		if len(r.cache) >= maxCacheEntries {
			r.cache = make(map[[32]byte]entry)
		}
	}
	r.cache[sum] = e
}

// IsRevoked consulta la marca de revocacion de una clave ya resuelta.
func (r *Resolver) IsRevoked(ctx context.Context, keyID string) bool {
	if r.revocations == nil {
		return false
	}
	revoked, err := r.revocations.IsRevoked(ctx, keyID)
	return err == nil && revoked
}

type resolveRequest struct {
	Token    string `json:"token"`
	ClientIP string `json:"client_ip,omitempty"`
}

func (r *Resolver) fetch(ctx context.Context, token, clientIP string) (*Principal, error) {
	body, err := json.Marshal(resolveRequest{Token: token, ClientIP: clientIP})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+resolvePath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if r.token != "" {
		req.Header.Set("X-Gateway-Token", r.token)
	}
	resp, err := r.client.Do(httpclient.Idempotent(req))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return nil, ErrInvalid
	default:
		return nil, fmt.Errorf("%w: access-control respondió %d", ErrUnavailable, resp.StatusCode)
	}
	var payload struct {
		Data Principal `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("%w: respuesta ilegible: %v", ErrUnavailable, err)
	}
	p := payload.Data
	if p.ID == "" || p.TenantID == "" || len(p.Scopes) == 0 {
		return nil, fmt.Errorf("%w: respuesta incompleta", ErrUnavailable)
	}
	if _, ok := middleware.ParseAPIKeyScopes(middleware.FormatAPIKeyScopes(p.Scopes)); !ok {
		return nil, fmt.Errorf("%w: alcance ilegible", ErrUnavailable)
	}
	return &p, nil
}
