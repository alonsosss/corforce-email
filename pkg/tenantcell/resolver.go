package tenantcell

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/mailcell"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// Resolver nunca adivina: sin una respuesta de organization, reciente o la ultima conocida
// dentro de staleGrace cuando organization no responde, no hay celda.
//
// Una empresa no cambia de celda mientras no exista el traslado (Modelo_de_Datos_y_Celdas.md,
// 5), y un dominio vive en la celda de su empresa: el TTL acota cuanto tarda en verse una baja o
// un dominio nuevo, no un cambio de celda.

const (
	cacheTTL       = 5 * time.Minute
	negativeTTL    = 30 * time.Second
	staleGrace     = time.Hour
	cacheMax       = 10000
	resolveTimeout = 3 * time.Second
	maxResponse    = 4 << 10
)

// staleAnswers cuenta las resoluciones respondidas con la ultima celda conocida porque
// organization no respondio: un valor sostenido es organization caido.
var staleAnswers = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "cell_resolution_stale_total",
	Help: "Resoluciones de celda (de una empresa o de un dominio de correo) respondidas con la ultima celda conocida porque organization no respondio.",
})

func init() { prometheus.MustRegister(staleAnswers) }

// subject es lo que se resuelve: como se normaliza la clave, donde la sirve organization, que
// campo de la respuesta la repite y que respuesta es la negativa definitiva.
type subject struct {
	field        string
	normalize    func(string) (string, bool)
	path         func(key string) string
	notFoundCode string
	notFound     error
}

var tenantSubject = subject{
	field: "tenant_id",
	normalize: func(raw string) (string, bool) {
		id, err := uuid.Parse(raw)
		if err != nil {
			return "", false
		}
		return id.String(), true
	},
	path:         func(id string) string { return "/internal/organization/tenants/" + id + "/cell" },
	notFoundCode: "TENANT_NOT_FOUND",
	notFound:     ErrUnknownTenant,
}

var domainSubject = subject{
	field:     "domain",
	normalize: mailcell.NormalizeDomain,
	path: func(name string) string {
		return "/internal/organization/mail-domains/" + url.PathEscape(name) + "/cell"
	},
	notFoundCode: "MAIL_DOMAIN_NOT_FOUND",
	notFound:     ErrUnknownDomain,
}

type entry struct {
	key string
	// cell vacia: organization respondio que la clave no existe.
	cell    string
	fetched time.Time
}

func (e entry) freshUntil() time.Time {
	if e.cell == "" {
		return e.fetched.Add(negativeTTL)
	}
	return e.fetched.Add(cacheTTL)
}

// usableWhenDown: una respuesta positiva sigue valiendo, con organization caido, hasta
// staleGrace despues de caducar. Una negativa nunca.
func (e entry) usableWhenDown(now time.Time) bool {
	return e.cell != "" && now.Before(e.freshUntil().Add(staleGrace))
}

func (e entry) answer(notFound error) (string, error) {
	if e.cell == "" {
		return "", notFound
	}
	return e.cell, nil
}

// lookup es una consulta en curso: las peticiones de la misma clave que llegan mientras tanto
// esperan su respuesta en vez de lanzar otra.
type lookup struct {
	done chan struct{}
	cell string
	err  error
}

// Resolver resuelve la celda de una empresa o de un dominio de correo contra organization con
// cache LRU acotada: 5 minutos la respuesta positiva, 30 s la negativa y una sola consulta para
// las peticiones simultaneas de la misma clave.
type Resolver struct {
	subject subject
	baseURL string
	token   string
	client  *http.Client
	logger  *zap.Logger
	now     func() time.Time
	max     int

	mu       sync.Mutex
	entries  map[string]*list.Element
	order    *list.List
	inflight map[string]*lookup
}

// NewResolver resuelve la celda de cada empresa preguntando a organization en organizationURL
// (URL interna, sin la ruta) con el token interno.
func NewResolver(organizationURL, token string, logger *zap.Logger) *Resolver {
	return newResolver(tenantSubject, organizationURL, token, logger)
}

// NewDomainResolver resuelve la celda de cada dominio de correo activo con el indice global
// dominio -> celda de organization. Sus errores son ErrUnknownDomain y ErrUnresolved.
func NewDomainResolver(organizationURL, token string, logger *zap.Logger) *Resolver {
	return newResolver(domainSubject, organizationURL, token, logger)
}

func newResolver(s subject, organizationURL, token string, logger *zap.Logger) *Resolver {
	return &Resolver{
		subject:  s,
		baseURL:  strings.TrimRight(organizationURL, "/"),
		token:    token,
		client:   &http.Client{Timeout: resolveTimeout},
		logger:   logger,
		now:      time.Now,
		max:      cacheMax,
		entries:  make(map[string]*list.Element),
		order:    list.New(),
		inflight: make(map[string]*lookup),
	}
}

// CellOf devuelve el codigo de la celda de la clave (una empresa o un dominio, segun el
// Resolver), la negativa definitiva (ErrUnknownTenant o ErrUnknownDomain) o ErrUnresolved. Una
// clave sin la forma debida es la negativa sin preguntar a nadie.
func (c *Resolver) CellOf(ctx context.Context, raw string) (string, error) {
	key, ok := c.subject.normalize(raw)
	if !ok {
		return "", c.subject.notFound
	}

	c.mu.Lock()
	cachedEntry, cached := c.get(key)
	if cached && c.now().Before(cachedEntry.freshUntil()) {
		c.mu.Unlock()
		return cachedEntry.answer(c.subject.notFound)
	}
	look, running := c.inflight[key]
	if !running {
		look = &lookup{done: make(chan struct{})}
		c.inflight[key] = look
		go c.lookup(key, look)
	}
	c.mu.Unlock()

	select {
	case <-look.done:
	case <-ctx.Done():
		return "", ErrUnresolved
	}
	if look.err == nil || errors.Is(look.err, c.subject.notFound) {
		return look.cell, look.err
	}
	if cached && cachedEntry.usableWhenDown(c.now()) {
		staleAnswers.Inc()
		return cachedEntry.cell, nil
	}
	return "", ErrUnresolved
}

// lookup consulta a organization con su propio plazo: que el cliente que la lanzo corte no
// debe dejar sin respuesta a los que esperan la misma.
func (c *Resolver) lookup(key string, look *lookup) {
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	cell, err := c.fetch(ctx, key)

	c.mu.Lock()
	switch {
	case err == nil, errors.Is(err, c.subject.notFound):
		c.put(entry{key: key, cell: cell, fetched: c.now()})
	default:
		c.logger.Warn("celdas: organization no dio la celda", zap.String(c.subject.field, key), zap.Error(err))
	}
	delete(c.inflight, key)
	look.cell, look.err = cell, err
	c.mu.Unlock()
	close(look.done)
}

// cellPayload es el contrato de las rutas de celda de organization: data lleva la clave
// preguntada (tenant_id o domain) y cell_code.
type cellPayload struct {
	Data  map[string]string `json:"data"`
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

// fetch solo toma por definitiva la respuesta del contrato: 200 con la misma clave y un codigo
// de celda bien formado, o 404 con el codigo de la negativa. Cualquier otra, tambien el 404 de
// una ruta que organization no sirve, es "no se pudo determinar".
func (c *Resolver) fetch(ctx context.Context, key string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+c.subject.path(key), nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnresolved, err)
	}
	req.Header.Set("X-Gateway-Token", c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnresolved, err)
	}
	defer resp.Body.Close()

	var payload cellPayload
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, maxResponse)).Decode(&payload)
	switch {
	case resp.StatusCode == http.StatusNotFound && decodeErr == nil && payload.Error.Code == c.subject.notFoundCode:
		return "", c.subject.notFound
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("%w: organization respondio %d", ErrUnresolved, resp.StatusCode)
	case decodeErr != nil:
		return "", fmt.Errorf("%w: respuesta ilegible: %v", ErrUnresolved, decodeErr)
	case payload.Data[c.subject.field] != key || !ValidCode(payload.Data["cell_code"]):
		return "", fmt.Errorf("%w: respuesta fuera de contrato", ErrUnresolved)
	}
	return payload.Data["cell_code"], nil
}

// get y put mantienen la cache como LRU acotada a max entradas. Con c.mu tomado.
func (c *Resolver) get(key string) (entry, bool) {
	el, ok := c.entries[key]
	if !ok {
		return entry{}, false
	}
	c.order.MoveToFront(el)
	return el.Value.(entry), true
}

func (c *Resolver) put(e entry) {
	if el, ok := c.entries[e.key]; ok {
		el.Value = e
		c.order.MoveToFront(el)
		return
	}
	c.entries[e.key] = c.order.PushFront(e)
	for c.order.Len() > c.max {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(entry).key)
	}
}
