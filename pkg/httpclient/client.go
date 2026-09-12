// Package httpclient da a las llamadas servicio-a-servicio un comportamiento uniforme
// frente a fallos: timeout por intento, reintentos con backoff SOLO donde repetir es
// seguro, y un cortacircuitos por destino para no apilar peticiones contra un servicio
// caido (que es como una indisponibilidad puntual se convierte en una cascada).
//
// Regla de seguridad de los reintentos: por defecto solo se repiten los metodos
// idempotentes (GET, HEAD, PUT, DELETE). Un POST NO se repite salvo que quien lo emite
// lo declare idempotente con Idempotent(req) — repetir "consumir stock" o "emitir
// comprobante" duplicaria el efecto, y un timeout no dice si la peticion llego o no.
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

// ErrCircuitOpen se devuelve mientras el cortacircuitos del destino esta abierto. El
// llamador sabe asi que no llego a intentarse, y puede degradar en vez de esperar.
var ErrCircuitOpen = errors.New("circuito abierto por fallos consecutivos del destino")

type Options struct {
	// Timeout de CADA intento (no del conjunto). El contexto del llamador sigue
	// mandando sobre el total.
	Timeout time.Duration
	// MaxAttempts incluye el primer intento. 1 = sin reintentos.
	MaxAttempts int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	// FailureThreshold: fallos consecutivos (transporte o 5xx) que abren el circuito.
	FailureThreshold int
	// Cooldown: cuanto permanece abierto antes de dejar pasar un intento de prueba.
	Cooldown time.Duration
}

func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 3
	}
	if o.BaseBackoff <= 0 {
		o.BaseBackoff = 100 * time.Millisecond
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = 2 * time.Second
	}
	if o.FailureThreshold <= 0 {
		o.FailureThreshold = 5
	}
	if o.Cooldown <= 0 {
		o.Cooldown = 30 * time.Second
	}
	return o
}

// Client envuelve un *http.Client. Un Client por destino: el estado del cortacircuitos
// es por servicio llamado, no global.
type Client struct {
	name string
	http *http.Client
	opt  Options

	mu        sync.Mutex
	failures  int
	openUntil time.Time
}

func New(name string, opt Options) *Client {
	opt = opt.withDefaults()
	return &Client{
		name: name,
		http: &http.Client{Timeout: opt.Timeout},
		opt:  opt,
	}
}

type idempotentKey struct{}

// Idempotent marca una peticion como segura de repetir aunque su metodo no lo sea.
// Usarlo solo cuando el endpoint destino deduplica (clave de idempotencia, upsert por
// referencia) o cuando repetir no tiene efecto observable.
func Idempotent(req *http.Request) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), idempotentKey{}, true))
}

func isIdempotent(req *http.Request) bool {
	if v, ok := req.Context().Value(idempotentKey{}).(bool); ok && v {
		return true
	}
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
		return true
	}
	return false
}

// retryableStatus son los codigos que indican indisponibilidad temporal del destino.
// Un 500 NO se reintenta: puede haber dejado efecto parcial y repetirlo no ayuda.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// Do ejecuta la peticion aplicando cortacircuitos y, si procede, reintentos.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if err := c.checkCircuit(); err != nil {
		return nil, err
	}

	retryable := isIdempotent(req) && c.opt.MaxAttempts > 1
	// Sin GetBody no se puede rebobinar el cuerpo para un segundo intento.
	if retryable && req.Body != nil && req.GetBody == nil {
		retryable = false
	}
	attempts := 1
	if retryable {
		attempts = c.opt.MaxAttempts
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := c.sleepBackoff(req.Context(), attempt); err != nil {
				return nil, err
			}
			clone, err := rewind(req)
			if err != nil {
				return nil, err
			}
			req = clone
		}

		resp, err := c.http.Do(req)
		if err != nil {
			// La cancelacion del llamador no es un fallo del destino.
			if req.Context().Err() != nil {
				return nil, err
			}
			c.recordFailure()
			lastErr = fmt.Errorf("%s: %w", c.name, err)
			continue
		}
		if resp.StatusCode >= 500 {
			c.recordFailure()
			if retryableStatus(resp.StatusCode) && attempt < attempts-1 {
				drain(resp)
				lastErr = fmt.Errorf("%s: status %d", c.name, resp.StatusCode)
				continue
			}
			return resp, nil
		}
		// Cualquier respuesta por debajo de 500 es el destino respondiendo: sano.
		c.recordSuccess()
		return resp, nil
	}
	return nil, lastErr
}

func (c *Client) sleepBackoff(ctx context.Context, attempt int) error {
	backoff := c.opt.BaseBackoff << (attempt - 1)
	if backoff > c.opt.MaxBackoff {
		backoff = c.opt.MaxBackoff
	}
	// Jitter: evita que todas las replicas reintenten en el mismo instante.
	wait := backoff/2 + time.Duration(rand.Int63n(int64(backoff/2)+1))
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) checkCircuit() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.openUntil.IsZero() || time.Now().After(c.openUntil) {
		return nil
	}
	return fmt.Errorf("%s: %w", c.name, ErrCircuitOpen)
}

func (c *Client) recordFailure() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures++
	if c.failures >= c.opt.FailureThreshold {
		c.openUntil = time.Now().Add(c.opt.Cooldown)
		// El siguiente intento tras el cooldown vuelve a contar desde cero: si vuelve a
		// fallar, un solo fallo reabre el circuito.
		c.failures = c.opt.FailureThreshold - 1
	}
}

func (c *Client) recordSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures = 0
	c.openUntil = time.Time{}
}

// rewind devuelve una copia de la peticion con el cuerpo reposicionado al inicio.
func rewind(req *http.Request) (*http.Request, error) {
	clone := req.Clone(req.Context())
	if req.GetBody == nil {
		return clone, nil
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, fmt.Errorf("rebobinar cuerpo: %w", err)
	}
	clone.Body = body
	return clone, nil
}

func drain(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_ = resp.Body.Close()
}
