package maildirectorycli

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/ports"
)

// primarySendersCacheMaxEntries acota la memoria: pasado el tope se descartan las entradas vencidas.
const primarySendersCacheMaxEntries = 4096

// CachedPrimarySenders guarda por buzon los remitentes fijados durante ttl. La bandeja los lee en
// cada listado de INBOX, y todas las llamadas del webmail a mail-directory llegan desde una misma
// IP bajo su limite por minuto: sin cache, abrir la bandeja gasta ese cupo y deja sin servicio los
// recordatorios, los envios programados y las reservas. Fijar o quitar un remitente invalida la
// entrada de esta instancia; otra replica lo ve al vencer el ttl.
type CachedPrimarySenders struct {
	next ports.PrimarySenderDirectory
	ttl  time.Duration
	now  func() time.Time

	mu      sync.Mutex
	entries map[string]cachedSenders
}

type cachedSenders struct {
	addresses []string
	expires   time.Time
}

// NewCachedPrimarySenders envuelve next. Con ttl no positivo no cachea nada.
func NewCachedPrimarySenders(next ports.PrimarySenderDirectory, ttl time.Duration) *CachedPrimarySenders {
	return &CachedPrimarySenders{next: next, ttl: ttl, now: time.Now, entries: map[string]cachedSenders{}}
}

func (c *CachedPrimarySenders) PrimarySenders(ctx context.Context, username string) ([]string, error) {
	if c.ttl <= 0 {
		return c.next.PrimarySenders(ctx, username)
	}
	now := c.now()
	c.mu.Lock()
	if e, ok := c.entries[username]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		return slices.Clone(e.addresses), nil
	}
	c.mu.Unlock()

	addresses, err := c.next.PrimarySenders(ctx, username)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if len(c.entries) >= primarySendersCacheMaxEntries {
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
		}
	}
	if len(c.entries) < primarySendersCacheMaxEntries {
		c.entries[username] = cachedSenders{addresses: slices.Clone(addresses), expires: now.Add(c.ttl)}
	}
	c.mu.Unlock()
	return addresses, nil
}

func (c *CachedPrimarySenders) AddPrimarySender(ctx context.Context, username, address string) error {
	err := c.next.AddPrimarySender(ctx, username, address)
	c.invalidate(username)
	return err
}

func (c *CachedPrimarySenders) RemovePrimarySender(ctx context.Context, username, address string) error {
	err := c.next.RemovePrimarySender(ctx, username, address)
	c.invalidate(username)
	return err
}

func (c *CachedPrimarySenders) invalidate(username string) {
	c.mu.Lock()
	delete(c.entries, username)
	c.mu.Unlock()
}
