// Package loki consulta el API HTTP de Loki (GET /loki/api/v1/query_range) con un plazo corto y una
// respuesta acotada en bytes. Recibe la consulta LogQL ya construida por el dominio: aqui no se compone
// nada con texto del cliente.
package loki

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/observability/internal/domain"
)

const (
	queryRangePath = "/loki/api/v1/query_range"
	// maxBody acota lo que se lee de Loki: 500 lineas de registro caben de sobra en 4 MiB; una respuesta
	// mayor se rechaza en vez de cargarla en memoria.
	maxBody = 4 << 20
	timeout = 10 * time.Second
)

type Client struct {
	baseURL string
	client  *httpclient.Client
}

// New recibe la URL base de Loki (config.ServiceURL: scheme://host[:port]).
func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		client:  httpclient.New("loki", httpclient.Options{Timeout: timeout, MaxAttempts: 1}),
	}
}

// QueryRange ejecuta la consulta en la ventana y devuelve las lineas de todos los flujos, sin ordenar.
func (c *Client) QueryRange(ctx context.Context, query string, since, until time.Time, limit int, direction domain.LogDirection) ([]domain.LogEntry, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("start", strconv.FormatInt(since.UnixNano(), 10))
	params.Set("end", strconv.FormatInt(until.UnixNano(), 10))
	params.Set("limit", strconv.Itoa(limit))
	params.Set("direction", string(direction))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+queryRangePath+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", domain.ErrStoreUnavailable, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", domain.ErrStoreUnavailable, err)
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("%w: la respuesta supera %d bytes", domain.ErrStoreRejected, maxBody)
	}
	switch {
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("%w: %d", domain.ErrStoreUnavailable, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%w: %d", domain.ErrStoreRejected, resp.StatusCode)
	}
	return decode(body)
}

// queryRangeResponse es la parte del formato de Loki que se lee: flujos con sus etiquetas y sus valores
// [instante en nanosegundos, linea].
type queryRangeResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func decode(body []byte) ([]domain.LogEntry, error) {
	var out queryRangeResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%w: respuesta ilegible: %w", domain.ErrStoreRejected, err)
	}
	if out.Status != "success" {
		return nil, fmt.Errorf("%w: estado %q", domain.ErrStoreRejected, out.Status)
	}
	if out.Data.ResultType != "streams" {
		return nil, fmt.Errorf("%w: tipo de resultado %q", domain.ErrStoreRejected, out.Data.ResultType)
	}
	entries := make([]domain.LogEntry, 0)
	for _, stream := range out.Data.Result {
		for _, v := range stream.Values {
			ns, err := strconv.ParseInt(v[0], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%w: instante ilegible %q", domain.ErrStoreRejected, v[0])
			}
			entries = append(entries, domain.LogEntry{Timestamp: time.Unix(0, ns).UTC(), Line: v[1], Labels: labelsOf(stream.Stream)})
		}
	}
	return entries, nil
}

// labelsOf conserva las etiquetas que pone promtail y descarta cualquier otra que Loki anada.
func labelsOf(stream map[string]string) map[string]string {
	out := make(map[string]string, 3)
	for _, k := range []string{"contenedor", "plano", "flujo"} {
		if v, ok := stream[k]; ok {
			out[k] = v
		}
	}
	return out
}
