package tenantcell

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
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// Llamadas de un servicio a la instancia de un servicio de celda (mail-directory, mail-security)
// por una empresa: domain-service al activar dominios y entregar claves DKIM, organization al dar
// de baja el correo de una empresa. Falla cerrado: sin celda resuelta o sin instancia declarada no
// sale hacia ninguna, y el 403 TENANT_NOT_IN_CELL de una instancia es un error de configuracion,
// nunca un exito ni un 404 (Modelo_de_Datos_y_Celdas.md, 5.4).

// ErrNotInCell: la instancia rechazo a la empresa porque no es de su celda. Llegar a la
// instancia de otra celda es siempre un error de despliegue.
var ErrNotInCell = errors.New("la instancia no atiende a la empresa porque no es de su celda (TENANT_NOT_IN_CELL): revisar la configuración de celdas")

// Motivos, etiqueta reason de cell_call_failures_total.
const (
	callReasonUnresolved = "unresolved"
	callReasonUnknown    = "unknown_tenant"
	callReasonNotServed  = "not_served"
	callReasonNotInCell  = "not_in_cell"
)

const (
	defaultCallTimeout  = 5 * time.Second
	defaultCallAttempts = 3
	maxErrorBody        = 4 << 10
)

// callFailures lleva el servicio de celda de destino en cell_service: la etiqueta service la pone
// el recolector a quien llama.
var callFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "cell_call_failures_total",
	Help: "Llamadas a un servicio de celda por una empresa que no salieron hacia ninguna instancia (celda sin resolver, empresa desconocida, celda sin instancia declarada) o que la instancia rechazo porque la empresa no es de su celda.",
}, []string{"cell_service", "reason"})

func init() { prometheus.MustRegister(callFailures) }

// CallerOptions ajusta el cliente de cada instancia. Cero toma 5 s por intento y 3 intentos.
type CallerOptions struct {
	Timeout     time.Duration
	MaxAttempts int
}

// Caller hace las llamadas internas a un servicio de celda por una empresa: token de gateway y
// X-Tenant-ID, los reintentos de pkg/httpclient y un cortacircuitos por instancia, para que una
// celda caida no corte las llamadas a las demas.
type Caller struct {
	service string
	token   string
	targets *Targets
	clients map[string]*httpclient.Client
	logger  *zap.Logger
}

// NewCaller llama al servicio de celda service por las instancias de targets.
func NewCaller(service string, targets *Targets, token string, logger *zap.Logger, opts CallerOptions) *Caller {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultCallTimeout
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = defaultCallAttempts
	}
	clients := map[string]*httpclient.Client{}
	for _, target := range targets.URLs() {
		clients[target] = httpclient.New(service, httpclient.Options{Timeout: opts.Timeout, MaxAttempts: opts.MaxAttempts})
	}
	// Las series nacen a cero: un contador que aparece ya en 1 no da increase().
	for _, reason := range []string{callReasonUnresolved, callReasonUnknown, callReasonNotServed, callReasonNotInCell} {
		callFailures.WithLabelValues(service, reason)
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Caller{service: service, token: token, targets: targets, clients: clients, logger: logger}
}

// Do envia la peticion a la instancia de la celda de la empresa, que resuelve Targets.For; path es
// la ruta del servicio. Devuelve la respuesta de la instancia, que cierra quien llama, salvo que no
// haya instancia a la que llamar o que la instancia rechace a la empresa por no ser de su celda: en
// los dos casos no hay respuesta y el error lo dice.
func (c *Caller) Do(ctx context.Context, tenantID uuid.UUID, method, path string, body []byte) (*http.Response, error) {
	target, cell, err := c.targets.For(ctx, tenantID.String())
	if err != nil {
		return nil, c.refuse(tenantID, cell, callReasonOf(err), err)
	}
	return c.send(ctx, target, cell, tenantID, method, path, body)
}

// DoInCell es Do para quien ya sabe la celda de la empresa (Targets.ForCell): no pregunta a nadie.
func (c *Caller) DoInCell(ctx context.Context, cell string, tenantID uuid.UUID, method, path string, body []byte) (*http.Response, error) {
	target, err := c.targets.ForCell(cell)
	if err != nil {
		return nil, c.refuse(tenantID, cell, callReasonOf(err), err)
	}
	return c.send(ctx, target, cell, tenantID, method, path, body)
}

func (c *Caller) send(ctx context.Context, target, cell string, tenantID uuid.UUID, method, path string, body []byte) (*http.Response, error) {
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
	if resp.StatusCode == http.StatusForbidden && ErrorCode(resp) == CodeNotInCell {
		resp.Body.Close()
		return nil, c.refuse(tenantID, cell, callReasonNotInCell, ErrNotInCell)
	}
	return resp, nil
}

func callReasonOf(err error) string {
	switch {
	case errors.Is(err, ErrNotServed):
		return callReasonNotServed
	case errors.Is(err, ErrUnknownTenant):
		return callReasonUnknown
	default:
		return callReasonUnresolved
	}
}

// refuse cuenta y registra una llamada que no llego a la instancia de la celda de la empresa.
// Una celda sin instancia o una instancia de otra celda son errores de despliegue; sin celda
// resuelta, organization no respondio y el paso se reintenta.
func (c *Caller) refuse(tenantID uuid.UUID, cell, reason string, cause error) error {
	callFailures.WithLabelValues(c.service, reason).Inc()
	fields := []zap.Field{
		zap.String("service", c.service), zap.String("reason", reason),
		zap.String("tenant_id", tenantID.String()), zap.String("cell", cell), zap.Error(cause),
	}
	if reason == callReasonNotServed || reason == callReasonNotInCell {
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
