package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/observability/internal/domain"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

var ahora = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

type almacen struct {
	mu       sync.Mutex
	Query    string
	Since    time.Time
	Until    time.Time
	Limit    int
	Dir      domain.LogDirection
	Entries  []domain.LogEntry
	Err      error
	Llamadas int
}

func (a *almacen) QueryRange(_ context.Context, query string, since, until time.Time, limit int, dir domain.LogDirection) ([]domain.LogEntry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Llamadas++
	a.Query, a.Since, a.Until, a.Limit, a.Dir = query, since, until, limit, dir
	return a.Entries, a.Err
}

type metricas struct {
	mu   sync.Mutex
	vals map[string]int
}

func (m *metricas) LogQueryObserved(service, outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.vals == nil {
		m.vals = map[string]int{}
	}
	m.vals[service+"/"+outcome]++
}

func nuevo(store *almacen) (*LogsUseCase, *metricas, *observer.ObservedLogs) {
	core, logs := observer.New(zap.InfoLevel)
	m := &metricas{}
	deps := LogsDeps{Metrics: m, Logger: zap.New(core), Now: func() time.Time { return ahora }}
	if store != nil {
		deps.Store = store
	}
	return NewLogsUseCase(deps), m, logs
}

func TestSoloElOperadorDeLaPlataformaConsulta(t *testing.T) {
	store := &almacen{}
	uc, m, _ := nuevo(store)
	if _, err := uc.Services(false); !errors.Is(err, domain.ErrPlatformOnly) {
		t.Fatalf("Services: %v", err)
	}
	if _, err := uc.Query(context.Background(), false, "u", domain.LogQueryInput{Service: "gateway"}); !errors.Is(err, domain.ErrPlatformOnly) {
		t.Fatalf("Query: %v", err)
	}
	if store.Llamadas != 0 || len(m.vals) != 0 {
		t.Fatalf("un administrador de empresa no debe llegar al almacen: %+v %v", store, m.vals)
	}
	svcs, err := uc.Services(true)
	if err != nil || len(svcs) == 0 {
		t.Fatalf("Services: %v %v", svcs, err)
	}
}

func TestSinAlmacenEs503YSeAnotaTrasValidar(t *testing.T) {
	uc, m, _ := nuevo(nil)
	if _, err := uc.Query(context.Background(), true, "u", domain.LogQueryInput{Service: "gateway"}); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("%v", err)
	}
	if _, err := uc.Query(context.Background(), true, "u", domain.LogQueryInput{Service: "nadie"}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("la validacion va antes que la configuracion: %v", err)
	}
	if m.vals["gateway/not_configured"] != 1 || m.vals["unknown/invalid"] != 1 {
		t.Fatalf("metricas: %v", m.vals)
	}
}

func TestLaConsultaLlegaConstruidaPorElDominioYElRegistroNuncaLlevaElTexto(t *testing.T) {
	store := &almacen{Entries: []domain.LogEntry{
		{Timestamp: ahora.Add(-time.Minute), Line: "vieja"}, {Timestamp: ahora, Line: "nueva"},
	}}
	uc, m, logs := nuevo(store)
	secreto := `ana.privada@cliente.test`
	since := ahora.Add(-2 * time.Hour)
	page, err := uc.Query(context.Background(), true, "user-42", domain.LogQueryInput{Service: "postfix-mail", Text: secreto, Since: &since, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if store.Query != `{servicio="postfix-mail"} |= "ana.privada@cliente.test"` || store.Since != since || store.Until != ahora || store.Limit != 1 || store.Dir != domain.DirectionBackward {
		t.Fatalf("al almacen llego: %+v", store)
	}
	if len(page.Entries) != 1 || page.Entries[0].Line != "nueva" || !page.Truncated || page.Service != "postfix-mail" || page.Limit != 1 {
		t.Fatalf("pagina: %+v", page)
	}
	if m.vals["postfix-mail/ok"] != 1 {
		t.Fatalf("metricas: %v", m.vals)
	}
	entradas := logs.All()
	if len(entradas) != 1 || entradas[0].Message != "consulta de registros" {
		t.Fatalf("registro: %+v", entradas)
	}
	todo := fmt.Sprintf("%s %v", entradas[0].Message, entradas[0].ContextMap())
	if strings.Contains(todo, secreto) || strings.Contains(todo, "privada") {
		t.Fatalf("el registro lleva el texto buscado: %s", todo)
	}
	ctx := entradas[0].ContextMap()
	if ctx["actor"] != "user-42" || ctx["service"] != "postfix-mail" || ctx["filtered"] != true || ctx["entries"] != int64(1) {
		t.Fatalf("contexto del registro: %v", ctx)
	}
}

func TestUnFalloDelAlmacenSeDistingueDeUnaConsultaRechazada(t *testing.T) {
	store := &almacen{Err: fmt.Errorf("%w: 503", domain.ErrStoreUnavailable)}
	uc, m, logs := nuevo(store)
	secreto := "texto-buscado"
	if _, err := uc.Query(context.Background(), true, "u", domain.LogQueryInput{Service: "gateway", Text: secreto}); !errors.Is(err, domain.ErrStoreUnavailable) {
		t.Fatalf("%v", err)
	}
	store.Err = fmt.Errorf("%w: 400", domain.ErrStoreRejected)
	if _, err := uc.Query(context.Background(), true, "u", domain.LogQueryInput{Service: "gateway", Text: secreto}); !errors.Is(err, domain.ErrStoreRejected) {
		t.Fatalf("%v", err)
	}
	if m.vals["gateway/unavailable"] != 1 || m.vals["gateway/error"] != 1 {
		t.Fatalf("metricas: %v", m.vals)
	}
	for _, e := range logs.All() {
		if strings.Contains(fmt.Sprint(e.Message, e.ContextMap()), secreto) {
			t.Fatalf("el registro de un fallo lleva el texto buscado: %v", e)
		}
	}
}

func TestSinLineasLaPaginaTraeUnaListaVaciaYNoEstaTruncada(t *testing.T) {
	uc, _, _ := nuevo(&almacen{})
	page, err := uc.Query(context.Background(), true, "u", domain.LogQueryInput{Service: "gateway"})
	if err != nil || page.Entries == nil || len(page.Entries) != 0 || page.Truncated {
		t.Fatalf("%+v %v", page, err)
	}
}
