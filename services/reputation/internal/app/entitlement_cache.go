package app

import (
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/google/uuid"
)

// entitlementCacheSweepSize: al llegar a este numero de entradas, cada alta nueva barre
// las caducadas. Hay dos por empresa como mucho, asi que es un techo holgado.
const entitlementCacheSweepSize = 4096

type entitlementEntry struct {
	ent      domain.Entitlement
	expires  time.Time
	consumed int64
}

// entitlementCache guarda la ultima respuesta de billing por empresa y clase y lo que este
// proceso autorizo desde entonces. Cada replica lleva la suya, asi que durante
// EntitlementTTL varias replicas pueden autorizar sobre el mismo remanente: es aceptable
// porque el derecho mensual no es una barrera de seguridad y billing lo cuenta despues por
// eventos.
type entitlementCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]*entitlementEntry
}

func newEntitlementCache(ttl time.Duration) *entitlementCache {
	return &entitlementCache{ttl: ttl, entries: make(map[string]*entitlementEntry)}
}

func entitlementKey(tenantID uuid.UUID, class domain.Class) string {
	return tenantID.String() + ":" + string(class)
}

// get devuelve la respuesta vigente y lo consumido desde que llego.
func (c *entitlementCache) get(key string, now time.Time) (domain.Entitlement, int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || !now.Before(e.expires) {
		return domain.Entitlement{}, 0, false
	}
	return e.ent, e.consumed, true
}

func (c *entitlementCache) put(key string, ent domain.Entitlement, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= entitlementCacheSweepSize {
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
		}
	}
	c.entries[key] = &entitlementEntry{ent: ent, expires: now.Add(c.ttl)}
}

// consume anota n mensajes autorizados contra la respuesta vigente.
func (c *entitlementCache) consume(key string, n int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		e.consumed += n
	}
}
