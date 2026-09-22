package loki

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/observability/internal/domain"
)

type captura struct {
	method, path, rawQuery string
	query                  string
	params                 map[string]string
}

// lokiFalso levanta un Loki de prueba que anota la peticion exacta y responde con handler.
func lokiFalso(t *testing.T, handler http.HandlerFunc) (*Client, *captura) {
	t.Helper()
	c := &captura{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.method, c.path, c.rawQuery = r.Method, r.URL.Path, r.URL.RawQuery
		c.params = map[string]string{}
		for k := range r.URL.Query() {
			c.params[k] = r.URL.Query().Get(k)
		}
		c.query = r.URL.Query().Get("query")
		handler(w, r)
	}))
	t.Cleanup(ts.Close)
	return New(ts.URL), c
}

const respuesta = `{"status":"success","data":{"resultType":"streams","result":[
 {"stream":{"servicio":"postfix-mail","contenedor":"app-postfix-mail-1","plano":"app","flujo":"stdout","filename":"/x"},
  "values":[["1758456000000000000","Sep 21 12:00:00 postfix/smtp[1]: to=<a@b.c>, status=sent"],["1758456002000000000","linea 2"]]},
 {"stream":{"servicio":"postfix-mail","contenedor":"app-postfix-mail-1","plano":"app","flujo":"stderr"},
  "values":[["1758456001000000000","stderr"]]}]}}`

func TestLaConsultaLlegaTalCualConLaVentanaEnNanosegundos(t *testing.T) {
	c, got := lokiFalso(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(respuesta)) })
	logql := `{servicio="postfix-mail"} |= "a\"b\\c"`
	since := time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC)
	until := since.Add(time.Hour)
	entries, err := c.QueryRange(context.Background(), logql, since, until, 200, domain.DirectionForward)
	if err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodGet || got.path != "/loki/api/v1/query_range" {
		t.Fatalf("%s %s", got.method, got.path)
	}
	if got.query != logql {
		t.Fatalf("la consulta llego alterada: %q", got.query)
	}
	want := map[string]string{"start": fmt.Sprint(since.UnixNano()), "end": fmt.Sprint(until.UnixNano()), "limit": "200", "direction": "forward"}
	for k, v := range want {
		if got.params[k] != v {
			t.Errorf("%s = %q, se esperaba %q (%s)", k, got.params[k], v, got.rawQuery)
		}
	}
	if len(got.params) != 5 {
		t.Fatalf("parametros de mas: %v", got.params)
	}
	if len(entries) != 3 {
		t.Fatalf("lineas: %+v", entries)
	}
	if entries[0].Timestamp != time.Unix(0, 1758456000000000000).UTC() || !strings.HasSuffix(entries[0].Line, "status=sent") {
		t.Fatalf("primera linea: %+v", entries[0])
	}
	if l := entries[0].Labels; l["contenedor"] != "app-postfix-mail-1" || l["plano"] != "app" || l["flujo"] != "stdout" || len(l) != 3 {
		t.Fatalf("etiquetas: %v", l)
	}
	if entries[2].Labels["flujo"] != "stderr" {
		t.Fatalf("segundo flujo: %+v", entries[2])
	}
}

func TestSinResultadosDevuelveUnaListaVaciaNoNil(t *testing.T) {
	c, _ := lokiFalso(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[]}}`))
	})
	entries, err := c.QueryRange(context.Background(), `{servicio="gateway"}`, time.Unix(0, 0), time.Unix(1, 0), 10, domain.DirectionBackward)
	if err != nil || entries == nil || len(entries) != 0 {
		t.Fatalf("%v %v", entries, err)
	}
}

func TestUn5xxOUnCorteEsIndisponibilidadYUn4xxRechazo(t *testing.T) {
	c, _ := lokiFalso(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	_, err := c.QueryRange(context.Background(), `{servicio="gateway"}`, time.Unix(0, 0), time.Unix(1, 0), 10, domain.DirectionBackward)
	if !errors.Is(err, domain.ErrStoreUnavailable) {
		t.Fatalf("502: %v", err)
	}
	c, _ = lokiFalso(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("parse error"))
	})
	_, err = c.QueryRange(context.Background(), `{servicio="gateway"}`, time.Unix(0, 0), time.Unix(1, 0), 10, domain.DirectionBackward)
	if !errors.Is(err, domain.ErrStoreRejected) || errors.Is(err, domain.ErrStoreUnavailable) {
		t.Fatalf("400: %v", err)
	}
	apagado := New("http://127.0.0.1:9")
	_, err = apagado.QueryRange(context.Background(), `{servicio="gateway"}`, time.Unix(0, 0), time.Unix(1, 0), 10, domain.DirectionBackward)
	if !errors.Is(err, domain.ErrStoreUnavailable) {
		t.Fatalf("sin escucha: %v", err)
	}
}

func TestUnaRespuestaIlegibleOMayorQueElTopeSeRechaza(t *testing.T) {
	for nombre, cuerpo := range map[string]string{
		"json roto":       `{"status":"success","data":{`,
		"estado de error": `{"status":"error","data":{"resultType":"streams","result":[]}}`,
		"no son flujos":   `{"status":"success","data":{"resultType":"matrix","result":[]}}`,
		"instante roto":   `{"status":"success","data":{"resultType":"streams","result":[{"stream":{},"values":[["ayer","x"]]}]}}`,
	} {
		c, _ := lokiFalso(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(cuerpo)) })
		_, err := c.QueryRange(context.Background(), `{servicio="gateway"}`, time.Unix(0, 0), time.Unix(1, 0), 10, domain.DirectionBackward)
		if !errors.Is(err, domain.ErrStoreRejected) {
			t.Errorf("%s: %v", nombre, err)
		}
	}
	c, _ := lokiFalso(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[{"stream":{},"values":[["1","`))
		_, _ = w.Write([]byte(strings.Repeat("x", maxBody)))
		_, _ = w.Write([]byte(`"]]}]}}`))
	})
	_, err := c.QueryRange(context.Background(), `{servicio="gateway"}`, time.Unix(0, 0), time.Unix(1, 0), 10, domain.DirectionBackward)
	if !errors.Is(err, domain.ErrStoreRejected) || !strings.Contains(err.Error(), "supera") {
		t.Fatalf("respuesta enorme: %v", err)
	}
}
