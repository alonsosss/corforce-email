package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testKey = "clave-de-prueba-de-al-menos-32-caracteres"

type fakeOps struct {
	listLimit int
	listing   Listing
	err       error
	applied   []string
	flushed   int
	block     chan struct{}
	started   chan struct{}
}

func (f *fakeOps) List(_ context.Context, limit int) (Listing, error) {
	f.listLimit = limit
	if f.started != nil {
		close(f.started)
	}
	if f.block != nil {
		<-f.block
	}
	return f.listing, f.err
}

func (f *fakeOps) Apply(_ context.Context, a Action, id string) error {
	f.applied = append(f.applied, string(a)+" "+id)
	return f.err
}

func (f *fakeOps) Flush(context.Context) error { f.flushed++; return f.err }

func newTestServer(ops operations) http.Handler {
	return NewServer(ops, testKey, slog.New(slog.NewTextHandler(io.Discard, nil))).Routes()
}

func call(h http.Handler, method, path, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSinClaveValidaNoSeEjecutaNada(t *testing.T) {
	ops := &fakeOps{}
	h := newTestServer(ops)
	for _, key := range []string{"", "otra", testKey + "x", strings.ToUpper(testKey)} {
		for _, r := range [][2]string{{"GET", "/v1/queue"}, {"POST", "/v1/queue/flush"}, {"POST", "/v1/queue/ABCDEF1234/delete"}} {
			if rec := call(h, r[0], r[1], key); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s con clave %q: %d", r[0], r[1], key, rec.Code)
			}
		}
	}
	if len(ops.applied) != 0 || ops.flushed != 0 || ops.listLimit != 0 {
		t.Fatalf("no debe ejecutarse nada: %+v", ops)
	}
	if rec := call(h, "GET", "/v1/queue", ""); strings.Contains(rec.Body.String(), "queue_id") {
		t.Fatal("una respuesta 401 no lleva datos")
	}
}

func TestListarAplicaElTopeYDevuelveLaCola(t *testing.T) {
	ops := &fakeOps{listing: Listing{Total: 3, Truncated: true, Items: []Message{{QueueID: "ABCDEF1234", Sender: "a@b.c"}}}}
	h := newTestServer(ops)
	rec := call(h, "GET", "/v1/queue", testKey)
	if rec.Code != http.StatusOK || ops.listLimit != defaultListLimit {
		t.Fatalf("por defecto: %d limit=%d", rec.Code, ops.listLimit)
	}
	var got Listing
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Total != 3 || !got.Truncated || got.Items[0].QueueID != "ABCDEF1234" {
		t.Fatalf("cuerpo: %s", rec.Body)
	}
	if call(h, "GET", "/v1/queue?limit=25", testKey); ops.listLimit != 25 {
		t.Fatalf("limit=25: %d", ops.listLimit)
	}
	for _, bad := range []string{"0", "-1", "x", "5001", ""} {
		if bad == "" {
			continue
		}
		if rec := call(h, "GET", "/v1/queue?limit="+bad, testKey); rec.Code != http.StatusBadRequest {
			t.Errorf("limit=%s: %d", bad, rec.Code)
		}
	}
}

func TestLasAccionesValidanElIdentificadorYLaAccion(t *testing.T) {
	ops := &fakeOps{}
	h := newTestServer(ops)
	for _, action := range []string{"retry", "hold", "unhold", "delete"} {
		if rec := call(h, "POST", "/v1/queue/ABCDEF1234/"+action, testKey); rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s", action, rec.Code, rec.Body)
		}
	}
	if got := strings.Join(ops.applied, ","); got != "retry ABCDEF1234,hold ABCDEF1234,unhold ABCDEF1234,delete ABCDEF1234" {
		t.Fatalf("aplicadas: %s", got)
	}
	ops.applied = nil
	for _, path := range []string{"/v1/queue/ABC/delete", "/v1/queue/ABCDEF1234/super_delete", "/v1/queue/ABC%3BDEF1234/delete", "/v1/queue/%2Dd/delete", "/v1/queue/ABCDEF1234/"} {
		if rec := call(h, "POST", path, testKey); rec.Code == http.StatusOK {
			t.Errorf("%s no debe aceptarse", path)
		}
	}
	if len(ops.applied) != 0 {
		t.Fatalf("no debe ejecutarse nada con entradas invalidas: %v", ops.applied)
	}
	if rec := call(h, "GET", "/v1/queue/ABCDEF1234/delete", testKey); rec.Code == http.StatusOK {
		t.Fatal("una accion no se hace con GET")
	}
	if rec := call(h, "DELETE", "/v1/queue/ABCDEF1234", testKey); rec.Code == http.StatusOK {
		t.Fatal("no existe DELETE sobre la cola")
	}
}

func TestVaciarLaColaDiferida(t *testing.T) {
	ops := &fakeOps{}
	if rec := call(newTestServer(ops), "POST", "/v1/queue/flush", testKey); rec.Code != http.StatusOK || ops.flushed != 1 {
		t.Fatalf("flush: %d %d", rec.Code, ops.flushed)
	}
}

func TestLosErroresNoFiltranLaSalidaDePostfix(t *testing.T) {
	ops := &fakeOps{err: errors.Join(ErrCommand, errors.New("postsuper: fatal: /var/spool/postfix/secreto"))}
	rec := call(newTestServer(ops), "POST", "/v1/queue/ABCDEF1234/delete", testKey)
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "secreto") || strings.Contains(rec.Body.String(), "/var/spool") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	ops.err = ErrNotFound
	if rec := call(newTestServer(ops), "POST", "/v1/queue/ABCDEF1234/hold", testKey); rec.Code != http.StatusNotFound {
		t.Fatalf("no encontrado: %d", rec.Code)
	}
}

func TestSoloUnaOperacionALaVez(t *testing.T) {
	ops := &fakeOps{block: make(chan struct{}), started: make(chan struct{})}
	h := newTestServer(ops)
	done := make(chan int)
	go func() { done <- call(h, "GET", "/v1/queue", testKey).Code }()
	<-ops.started
	if rec := call(h, "POST", "/v1/queue/ABCDEF1234/delete", testKey); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("con otra operacion en curso: %d", rec.Code)
	}
	close(ops.block)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("la primera termina bien: %d", code)
	}
	if rec := call(h, "POST", "/v1/queue/ABCDEF1234/delete", testKey); rec.Code != http.StatusOK {
		t.Fatalf("liberado el cupo, la siguiente entra: %d", rec.Code)
	}
}
