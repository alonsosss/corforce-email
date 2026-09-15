// Package cellcli lleva cada llamada de domain-service a un servicio de celda (mail-directory,
// mail-security) a la instancia que sirve la celda de la empresa, y falla cerrado: sin celda
// resuelta o sin instancia declarada no sale hacia ninguna, y el 403 TENANT_NOT_IN_CELL de una
// instancia es un error de configuracion, nunca un exito ni un 404 (Modelo_de_Datos_y_Celdas.md,
// 5.4).
package cellcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// ErrNotInCell: la instancia rechazo a la empresa porque no es de su celda. Llegar a la
// instancia de otra celda es siempre un error de despliegue.
var ErrNotInCell = errors.New("la instancia no atiende a la empresa porque no es de su celda (TENANT_NOT_IN_CELL): revisar la configuracion de celdas")

// Motivos, etiqueta reason de cell_call_failures_total.
const (
	reasonUnresolved = "unresolved"
	reasonUnknown    = "unknown_tenant"
	reasonNotServed  = "not_served"
	reasonNotInCell  = "not_in_cell"
)

const (
	callTimeout  = 5 * time.Second
	callAttempts = 3
	maxErrorBody = 4 << 10
)

var failures = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "cell_call_failures_total",
	Help: "Llamadas a un servicio de celda por una empresa que no salieron hacia ninguna instancia (celda sin resolver, empresa desconocida, celda sin instancia declarada) o que la instancia rechazo porque la empresa no es de su celda.",
}, []string{"cell_service", "reason"})

func init() { prometheus.MustRegister(failures) }

// Caller hace las llamadas internas a un servicio de celda por una empresa: token de gateway y
// X-Tenant-ID, los reintentos de pkg/httpclient y un cortacircuitos por instancia, para que una
// celda caida no corte las llamadas a las demas.
type Caller struct {
	service string
	token   string
	targets *tenantcell.Targets
	clients map[string]*httpclient.Client
	logger  *zap.Logger
}

// New llama al servicio de celda service por las instancias de targets.
func New(service string, targets *tenantcell.Targets, token string, logger *zap.Logger) *Caller {
	clients := map[string]*httpclient.Client{}
	for _, target := range targets.URLs() {
		clients[target] = httpclient.New(service, httpclient.Options{Timeout: callTimeout, MaxAttempts: callAttempts})
	}
	// Las series nacen a cero: un contador que aparece ya en 1 no da increase().
	for _, reason := range []string{reasonUnresolved, reasonUnknown, reasonNotServed, reasonNotInCell} {
		failures.WithLabelValues(service, reason)
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Caller{service: service, token: token, targets: targets, clients: clients, logger: logger}
}

// Do envia la peticion a la instancia de la celda de la empresa; path es la ruta del servicio.
// Devuelve la respuesta de la instancia, que cierra quien llama, salvo que no haya instancia a
// la que llamar o que la instancia rechace a la empresa por no ser de su celda: en los dos casos
// no hay respuesta y el error lo dice.
func (c *Caller) Do(ctx context.Context, tenantID uuid.UUID, method, path string, body []byte) (*http.Response, error) {
	target, cell, err := c.targets.For(ctx, tenantID.String())
	if err != nil {
		return nil, c.refuse(tenantID, cell, reasonOf(err), err)
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		// GetBody permite a pkg/httpclient rebobinar el cuerpo en un reintento.
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Gateway-Token", c.token)
	req.Header.Set("X-Tenant-ID", tenantID.String())

	resp, err := c.clients[target].Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusForbidden && ErrorCode(resp) == tenantcell.CodeNotInCell {
		resp.Body.Close()
		return nil, c.refuse(tenantID, cell, reasonNotInCell, ErrNotInCell)
	}
	return resp, nil
}

func reasonOf(err error) string {
	switch {
	case errors.Is(err, tenantcell.ErrNotServed):
		return reasonNotServed
	case errors.Is(err, tenantcell.ErrUnknownTenant):
		return reasonUnknown
	default:
		return reasonUnresolved
	}
}

// refuse cuenta y registra una llamada que no llego a la instancia de la celda de la empresa.
// Una celda sin instancia o una instancia de otra celda son errores de despliegue; sin celda
// resuelta, organization no respondio y el paso se reintenta.
func (c *Caller) refuse(tenantID uuid.UUID, cell, reason string, cause error) error {
	failures.WithLabelValues(c.service, reason).Inc()
	fields := []zap.Field{
		zap.String("service", c.service), zap.String("reason", reason),
		zap.String("tenant_id", tenantID.String()), zap.String("cell", cell), zap.Error(cause),
	}
	if reason == reasonNotServed || reason == reasonNotInCell {
		c.logger.Error("celdas: la llamada no llega a la instancia de la celda de la empresa; revisar la configuracion de celdas", fields...)
	} else {
		c.logger.Warn("celdas: la llamada no sale sin la celda de la empresa; se reintenta", fields...)
	}
	return fmt.Errorf("%s: %w", c.service, cause)
}

// ErrorCode devuelve el codigo del sobre de error JSON de la respuesta ({"error":{"code":...}}),
// o vacio si no lo lleva, y deja el cuerpo legible otra vez.
func ErrorCode(resp *http.Response) string {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return ""
	}
	return envelope.Error.Code
}
