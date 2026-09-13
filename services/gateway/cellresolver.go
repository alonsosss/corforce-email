package main

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// Celda de una empresa para el enrutado con sesion por celda.
//
// El token de acceso lleva la empresa y no su celda (organization.v_tenants excluye cell_id a
// proposito): el gateway la pregunta a organization por su API interna, con el token interno y
// sin usuario, y la guarda en una cache acotada. Nunca adivina: sin una respuesta de
// organization, reciente o la ultima conocida dentro de cellStaleGrace cuando organization no
// responde, la peticion no sale hacia ninguna celda.
//
// Una empresa no cambia de celda mientras no exista el traslado (Modelo_de_Datos_y_Celdas.md,
// 5): el TTL acota cuanto tarda en verse una baja, no un cambio de celda.

const (
	cellCacheTTL       = 5 * time.Minute
	cellNegativeTTL    = 30 * time.Second
	cellStaleGrace     = time.Hour
	cellCacheMax       = 10000
	cellResolveTimeout = 3 * time.Second
	maxCellResponse    = 4 << 10
)

var (
	// errUnknownTenant: organization respondio de forma definitiva que la empresa no existe,
	// o la sesion no trae una empresa valida.
	errUnknownTenant = errors.New("organization no conoce la empresa de la sesion")
	// errCellUnresolved: no hay respuesta aplicable de organization.
	errCellUnresolved = errors.New("no se pudo resolver la celda de la empresa")
)

// cellStaleAnswers cuenta las peticiones enrutadas con la ultima celda conocida porque
// organization no respondio: un valor sostenido es organization caido.
var cellStaleAnswers = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "cell_resolution_stale_total",
	Help: "Peticiones enrutadas con la ultima celda conocida de su empresa porque organization no respondio.",
})

func init() { prometheus.MustRegister(cellStaleAnswers) }

type cellEntry struct {
	tenantID string
	// cell vacia: organization respondio que la empresa no existe.
	cell    string
	fetched time.Time
}

func (e cellEntry) freshUntil() time.Time {
	if e.cell == "" {
		return e.fetched.Add(cellNegativeTTL)
	}
	return e.fetched.Add(cellCacheTTL)
}

// usableWhenDown: una respuesta positiva sigue valiendo, con organization caido, hasta
// cellStaleGrace despues de caducar. Una negativa nunca.
func (e cellEntry) usableWhenDown(now time.Time) bool {
	return e.cell != "" && now.Before(e.freshUntil().Add(cellStaleGrace))
}

// cellLookup es una consulta en curso: las peticiones de la misma empresa que llegan mientras
// tanto esperan su respuesta en vez de lanzar otra.
type cellLookup struct {
	done chan struct{}
	cell string
	err  error
}

type cellResolver struct {
	baseURL string
	token   string
	client  *http.Client
	logger  *zap.Logger
	now     func() time.Time
	max     int

	mu       sync.Mutex
	entries  map[string]*list.Element
	order    *list.List
	inflight map[string]*cellLookup
}

func newCellResolver(organizationURL, token string, logger *zap.Logger) *cellResolver {
	return &cellResolver{
		baseURL:  organizationURL,
		token:    token,
		client:   &http.Client{Timeout: cellResolveTimeout},
		logger:   logger,
		now:      time.Now,
		max:      cellCacheMax,
		entries:  make(map[string]*list.Element),
		order:    list.New(),
		inflight: make(map[string]*cellLookup),
	}
}

// cellOf devuelve el codigo de la celda de la empresa, errUnknownTenant o errCellUnresolved.
func (c *cellResolver) cellOf(ctx context.Context, tenantID string) (string, error) {
	id, err := uuid.Parse(tenantID)
	if err != nil {
		return "", errUnknownTenant
	}
	key := id.String()

	c.mu.Lock()
	entry, cached := c.get(key)
	if cached && c.now().Before(entry.freshUntil()) {
		c.mu.Unlock()
		return entry.answer()
	}
	look, running := c.inflight[key]
	if !running {
		look = &cellLookup{done: make(chan struct{})}
		c.inflight[key] = look
		go c.lookup(key, look)
	}
	c.mu.Unlock()

	select {
	case <-look.done:
	case <-ctx.Done():
		return "", errCellUnresolved
	}
	if look.err == nil || errors.Is(look.err, errUnknownTenant) {
		return look.cell, look.err
	}
	if cached && entry.usableWhenDown(c.now()) {
		cellStaleAnswers.Inc()
		return entry.cell, nil
	}
	return "", errCellUnresolved
}

func (e cellEntry) answer() (string, error) {
	if e.cell == "" {
		return "", errUnknownTenant
	}
	return e.cell, nil
}

// lookup consulta a organization con su propio plazo: que el cliente que la lanzo corte no
// debe dejar sin respuesta a los que esperan la misma.
func (c *cellResolver) lookup(key string, look *cellLookup) {
	ctx, cancel := context.WithTimeout(context.Background(), cellResolveTimeout)
	defer cancel()
	cell, err := c.fetch(ctx, key)

	c.mu.Lock()
	switch {
	case err == nil, errors.Is(err, errUnknownTenant):
		c.put(cellEntry{tenantID: key, cell: cell, fetched: c.now()})
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
func (c *cellResolver) fetch(ctx context.Context, tenantID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/internal/organization/tenants/"+tenantID+"/cell", nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errCellUnresolved, err)
	}
	req.Header.Set("X-Gateway-Token", c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errCellUnresolved, err)
	}
	defer resp.Body.Close()

	var payload tenantCellPayload
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, maxCellResponse)).Decode(&payload)
	switch {
	case resp.StatusCode == http.StatusNotFound && decodeErr == nil && payload.Error.Code == "TENANT_NOT_FOUND":
		return "", errUnknownTenant
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("%w: organization respondio %d", errCellUnresolved, resp.StatusCode)
	case decodeErr != nil:
		return "", fmt.Errorf("%w: respuesta ilegible: %v", errCellUnresolved, decodeErr)
	case payload.Data.TenantID != tenantID || !validCellCode(payload.Data.CellCode):
		return "", fmt.Errorf("%w: respuesta fuera de contrato", errCellUnresolved)
	}
	return payload.Data.CellCode, nil
}

// get y put mantienen la cache como LRU acotada a max entradas. Con c.mu tomado.
func (c *cellResolver) get(key string) (cellEntry, bool) {
	el, ok := c.entries[key]
	if !ok {
		return cellEntry{}, false
	}
	c.order.MoveToFront(el)
	return el.Value.(cellEntry), true
}

func (c *cellResolver) put(e cellEntry) {
	if el, ok := c.entries[e.tenantID]; ok {
		el.Value = e
		c.order.MoveToFront(el)
		return
	}
	c.entries[e.tenantID] = c.order.PushFront(e)
	for c.order.Len() > c.max {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(cellEntry).tenantID)
	}
}
