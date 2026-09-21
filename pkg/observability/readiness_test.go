package observability

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func get(t *testing.T, h http.Handler, path string) (int, readyResponse, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var out readyResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func TestReadyz_ReportsDependenciesWithoutLeakingErrors(t *testing.T) {
	h := WithOps(http.NotFoundHandler())

	code, out, _ := get(t, h, ReadyPath)
	if code != http.StatusOK || out.Status != "ready" {
		t.Fatalf("sin dependencias registradas debia estar listo: %d %+v", code, out)
	}

	RegisterReadiness("test:ok", func(context.Context) error { return nil })
	RegisterReadiness("test:down", func(context.Context) error {
		return errors.New("failed to connect to host=10.0.0.5 user=secret_user")
	})
	t.Cleanup(func() { UnregisterReadiness("test:ok"); UnregisterReadiness("test:down") })

	code, out, body := get(t, h, ReadyPath)
	if code != http.StatusServiceUnavailable || out.Status != "unready" {
		t.Fatalf("con una dependencia caida se esperaba 503 unready: %d %+v", code, out)
	}
	if out.Checks["test:ok"] != "ok" || out.Checks["test:down"] != "fail" {
		t.Fatalf("resultado por dependencia inesperado: %+v", out.Checks)
	}
	if strings.Contains(body, "10.0.0.5") || strings.Contains(body, "secret_user") {
		t.Fatalf("la respuesta filtra el texto del error: %s", body)
	}

	UnregisterReadiness("test:down")
	if code, out, _ = get(t, h, ReadyPath); code != http.StatusOK || out.Status != "ready" {
		t.Fatalf("al retirar la dependencia caida debia volver a estar listo: %d %+v", code, out)
	}
}

func TestReadyz_HungDependencyDoesNotHangTheRoute(t *testing.T) {
	RegisterReadiness("test:hung", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	t.Cleanup(func() { UnregisterReadiness("test:hung") })

	start := time.Now()
	code, out, _ := get(t, WithOps(http.NotFoundHandler()), ReadyPath)
	if code != http.StatusServiceUnavailable || out.Checks["test:hung"] != "fail" {
		t.Fatalf("una dependencia colgada debe dar 503: %d %+v", code, out)
	}
	if elapsed := time.Since(start); elapsed > readyCheckTimeout+time.Second {
		t.Fatalf("la ruta tardo %s con una dependencia colgada", elapsed)
	}
}

func TestHealthz_StaysLivenessOnly(t *testing.T) {
	RegisterReadiness("test:down", func(context.Context) error { return errors.New("down") })
	t.Cleanup(func() { UnregisterReadiness("test:down") })
	if code, _, _ := get(t, WithOps(http.NotFoundHandler()), HealthPath); code != http.StatusOK {
		t.Fatalf("/healthz no depende de las dependencias: %d", code)
	}
}
