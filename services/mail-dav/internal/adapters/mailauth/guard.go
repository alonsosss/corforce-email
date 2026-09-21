package mailauth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/ports"
)

// GuardConfig acota el costo de verificar credenciales. Cada verificacion en mail-auth es una comparacion de
// bcrypt (decenas de milisegundos de CPU) y ademas escribe el registro de inicios: un cliente que sincroniza
// hace muchas peticiones seguidas, y un atacante con una credencial valida (o con cualquiera, contra un
// buzon que existe) puede pedirlas a razon de miles por minuto.
type GuardConfig struct {
	// CacheTTL es cuanto se recuerda una verificacion CORRECTA (0 no recuerda nada). Es lo que tarda en
	// aplicarse el cambio de una contrasena, el de dav_access o la baja de un buzon.
	CacheTTL time.Duration
	// MaxCached acota las verificaciones recordadas a la vez.
	MaxCached int
	// MaxConcurrent acota las verificaciones que se hacen a la vez contra mail-auth, que es tambien el
	// que atiende a Dovecot: el DAV no puede dejarlo sin CPU para los inicios de IMAP.
	MaxConcurrent int
	// Wait es lo que una verificacion espera un turno antes de responder que el servicio no esta disponible.
	Wait time.Duration
}

// Guard decora una ports.Authenticator con una cache corta de aciertos y con un tope de concurrencia. Nunca
// recuerda un rechazo: la contrasena corregida debe entrar a la siguiente peticion, y el freno de fuerza
// bruta por buzon e IP sigue siendo el de mail-auth.
type Guard struct {
	inner ports.Authenticator
	cfg   GuardConfig
	now   func() time.Time
	slots chan struct{}
	key   [32]byte

	mu     sync.Mutex
	cached map[[32]byte]cachedPrincipal
}

type cachedPrincipal struct {
	principal domain.Principal
	expires   time.Time
}

func NewGuard(inner ports.Authenticator, cfg GuardConfig) (*Guard, error) {
	if inner == nil || cfg.MaxConcurrent < 1 || cfg.Wait <= 0 || cfg.CacheTTL < 0 || (cfg.CacheTTL > 0 && cfg.MaxCached < 1) {
		return nil, errors.New("configuracion del guarda de autenticacion invalida")
	}
	g := &Guard{inner: inner, cfg: cfg, now: time.Now, slots: make(chan struct{}, cfg.MaxConcurrent), cached: map[[32]byte]cachedPrincipal{}}
	if _, err := rand.Read(g.key[:]); err != nil {
		return nil, fmt.Errorf("clave de la cache de autenticacion: %w", err)
	}
	return g, nil
}

// digest es la clave de la cache: un HMAC con una clave que solo vive en este proceso, de modo que la
// contrasena no queda en memoria y el resumen no sirve fuera de el.
func (g *Guard) digest(username, password string) [32]byte {
	mac := hmac.New(sha256.New, g.key[:])
	mac.Write([]byte(strings.ToLower(strings.TrimSpace(username))))
	mac.Write([]byte{0})
	mac.Write([]byte(password))
	var out [32]byte
	mac.Sum(out[:0])
	return out
}

func (g *Guard) lookup(key [32]byte) (domain.Principal, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	entry, ok := g.cached[key]
	if !ok {
		return domain.Principal{}, false
	}
	if !g.now().Before(entry.expires) {
		delete(g.cached, key)
		return domain.Principal{}, false
	}
	return entry.principal, true
}

func (g *Guard) remember(key [32]byte, p domain.Principal) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.cached) >= g.cfg.MaxCached {
		now := g.now()
		for k, e := range g.cached {
			if !now.Before(e.expires) {
				delete(g.cached, k)
			}
		}
		if len(g.cached) >= g.cfg.MaxCached {
			return
		}
	}
	g.cached[key] = cachedPrincipal{principal: p, expires: g.now().Add(g.cfg.CacheTTL)}
}

func (g *Guard) Authenticate(ctx context.Context, username, password, remoteIP string) (domain.Principal, error) {
	var key [32]byte
	if g.cfg.CacheTTL > 0 {
		key = g.digest(username, password)
		if p, ok := g.lookup(key); ok {
			return p, nil
		}
	}
	timer := time.NewTimer(g.cfg.Wait)
	defer timer.Stop()
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	case <-timer.C:
		return domain.Principal{}, fmt.Errorf("%w: demasiadas verificaciones de credenciales a la vez", domain.ErrUnavailable)
	case <-ctx.Done():
		return domain.Principal{}, fmt.Errorf("%w: %v", domain.ErrUnavailable, ctx.Err())
	}
	p, err := g.inner.Authenticate(ctx, username, password, remoteIP)
	if err == nil && g.cfg.CacheTTL > 0 {
		g.remember(key, p)
	}
	return p, err
}
