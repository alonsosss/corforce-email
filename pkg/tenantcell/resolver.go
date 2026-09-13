package tenantcell

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// Resolver nunca adivina: sin una respuesta de organization, reciente o la ultima conocida
// dentro de staleGrace cuando organization no responde, no hay celda.
//
// Una empresa no cambia de celda mientras no exista el traslado (Modelo_de_Datos_y_Celdas.md,
// 5): el TTL acota cuanto tarda en verse una baja, no un cambio de celda.

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
	Help: "Resoluciones de celda respondidas con la ultima celda conocida de su empresa porque organization no respondio.",
})

func init() { prometheus.MustRegister(staleAnswers) }

type entry struct {
	tenantID string
	// cell vacia: organization respondio que la empresa no existe.
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

func (e entry) answer() (string, error) {
	if e.cell == "" {
		return "", ErrUnknownTenant
	}
	return e.cell, nil
}

// lookup es una consulta en curso: las peticiones de la misma empresa que llegan mientras
// tanto esperan su respuesta en vez de lanzar otra.
type lookup struct {
	done chan struct{}
	cell string
	err  error
}

// Resolver resuelve la celda de una empresa contra organization con cache LRU acotada: 5
// minutos la respuesta positiva, 30 s la negativa y una sola consulta para las peticiones
// simultaneas de la misma empresa.
type Resolver struct {
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

// NewResolver pregunta a organization en organizationURL (URL interna, sin la ruta) con el
// token interno.
func NewResolver(organizationURL, token string, logger *zap.Logger) *Resolver {
	return &Resolver{
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

// CellOf devuelve el codigo de la celda de la empresa, ErrUnknownTenant o ErrUnresolved.
func (c *Resolver) CellOf(ctx context.Context, tenantID string) (string, error) {
	id, err := uuid.Parse(tenantID)
	if err != nil {
		return "", ErrUnknownTenant
	}
	key := id.String()

	c.mu.Lock()
	cachedEntry, cached := c.get(key)
	if cached && c.now().Before(cachedEntry.freshUntil()) {
		c.mu.Unlock()
		return cachedEntry.answer()
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
	if look.err == nil || errors.Is(look.err, ErrUnknownTenant) {
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
	case err == nil, errors.Is(err, ErrUnknownTenant):
		c.put(entry{tenantID: key, cell: cell, fetched: c.now()})
	default:
		c.logger.Warn("celdas: organization no dio la celda de la empresa", zap.String("tenant_id", key), zap.Error(err))
	}
	delete(c.inflight, key)
	look.cell, look.err = cell, err
	c.mu.Unlock()
	close(look.done)
}

// tenantCellPayload es el contrato de GET /internal/organization/tenants/{id}/cell.
type tenantCellPayload struct {
	Data struct {
		TenantID string `json:"tenant_id"`
		CellCode string `json:"cell_code"`
	} `json:"data"`
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

// fetch solo toma por definitiva la respuesta del contrato: 200 con la misma empresa y un
// codigo de celda bien formado, o 404 TENANT_NOT_FOUND. Cualquier otra, tambien el 404 de una
// ruta que organization no sirve, es "no se pudo determinar".
func (c *Resolver) fetch(ctx context.Context, tenantID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/internal/organization/tenants/"+tenantID+"/cell", nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnresolved, err)
	}
	req.Header.Set("X-Gateway-Token", c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnresolved, err)
	}
	defer resp.Body.Close()

	var payload tenantCellPayload
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, maxResponse)).Decode(&payload)
	switch {
	case resp.StatusCode == http.StatusNotFound && decodeErr == nil && payload.Error.Code == "TENANT_NOT_FOUND":
		return "", ErrUnknownTenant
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("%w: organization respondio %d", ErrUnresolved, resp.StatusCode)
	case decodeErr != nil:
		return "", fmt.Errorf("%w: respuesta ilegible: %v", ErrUnresolved, decodeErr)
	case payload.Data.TenantID != tenantID || !ValidCode(payload.Data.CellCode):
		return "", fmt.Errorf("%w: respuesta fuera de contrato", ErrUnresolved)
	}
	return payload.Data.CellCode, nil
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
	if el, ok := c.entries[e.tenantID]; ok {
		el.Value = e
		c.order.MoveToFront(el)
		return
	}
	c.entries[e.tenantID] = c.order.PushFront(e)
	for c.order.Len() > c.max {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(entry).tenantID)
	}
}
