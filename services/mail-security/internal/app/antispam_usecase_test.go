package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func newAntispamUC() (*AntispamUseCase, *apptest.Antispam, *observer.ObservedLogs) {
	inspector := apptest.NewAntispam()
	core, logs := observer.New(zap.InfoLevel)
	return NewAntispamUseCase(inspector, zap.New(core)), inspector, logs
}

func filas(n int) []domain.RspamdHistoryRow {
	base := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	out := make([]domain.RspamdHistoryRow, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, domain.RspamdHistoryRow{ID: fmt.Sprintf("m%d", i), Time: base.Add(time.Duration(i) * time.Second)})
	}
	return out
}

func TestSoloElOperadorDeLaPlataformaLeeElAntispam(t *testing.T) {
	uc, inspector, _ := newAntispamUC()
	if _, err := uc.Stats(context.Background(), false, "u"); !errors.Is(err, domain.ErrPlatformOnly) {
		t.Fatalf("Stats: %v", err)
	}
	if _, err := uc.History(context.Background(), false, "u", 10); !errors.Is(err, domain.ErrPlatformOnly) {
		t.Fatalf("History: %v", err)
	}
	if inspector.StatReads != 0 || inspector.HistoryReads != 0 {
		t.Fatalf("un administrador de empresa no debe llegar al controller: %+v", inspector)
	}
}

func TestSinInspectorEsNotConfigured(t *testing.T) {
	uc := NewAntispamUseCase(nil, zap.NewNop())
	if _, err := uc.Stats(context.Background(), true, "u"); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("Stats: %v", err)
	}
	if _, err := uc.History(context.Background(), true, "u", 10); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("History: %v", err)
	}
}

func TestElHistorialSeDevuelveDelMasRecienteAlMasAntiguoYAcotado(t *testing.T) {
	uc, inspector, logs := newAntispamUC()
	inspector.Rows = filas(7)
	for limit, want := range map[int]int{0: 7, -1: 7, 3: 3, domain.MaxRspamdHistoryRows + 100: 7} {
		out, err := uc.History(context.Background(), true, "user-42", limit)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Rows) != want || out.Total != 7 || out.Truncated != (want < 7) {
			t.Fatalf("limit %d: %d filas, total %d, truncado %v", limit, len(out.Rows), out.Total, out.Truncated)
		}
		if out.Rows[0].ID != "m6" || out.Rows[len(out.Rows)-1].ID != fmt.Sprintf("m%d", 7-want) {
			t.Fatalf("limit %d: orden %s..%s", limit, out.Rows[0].ID, out.Rows[len(out.Rows)-1].ID)
		}
	}
	entry := logs.All()[0]
	if entry.Message != "consulta del antispam" || entry.ContextMap()["actor"] != "user-42" || entry.ContextMap()["kind"] != "history" {
		t.Fatalf("registro: %+v", entry)
	}
}

func TestElTopeDelHistorialLoFijaElDominio(t *testing.T) {
	uc, inspector, _ := newAntispamUC()
	inspector.Rows = filas(domain.MaxRspamdHistoryRows + 5)
	out, err := uc.History(context.Background(), true, "u", 100000)
	if err != nil || len(out.Rows) != domain.MaxRspamdHistoryRows || !out.Truncated {
		t.Fatalf("%d filas, truncado %v, %v", len(out.Rows), out.Truncated, err)
	}
	out, err = uc.History(context.Background(), true, "u", 0)
	if err != nil || len(out.Rows) != domain.DefaultRspamdHistoryRows {
		t.Fatalf("por defecto: %d filas, %v", len(out.Rows), err)
	}
}

func TestLasEstadisticasLleganTalCualYUnFalloDelControllerSeDevuelve(t *testing.T) {
	uc, inspector, logs := newAntispamUC()
	inspector.Stat.Scanned = 42
	out, err := uc.Stats(context.Background(), true, "user-42")
	if err != nil || out.Scanned != 42 {
		t.Fatalf("%+v %v", out, err)
	}
	if logs.All()[0].ContextMap()["kind"] != "stats" {
		t.Fatalf("registro: %+v", logs.All()[0])
	}
	inspector.Err = fmt.Errorf("%w: 500", domain.ErrEngineUnreachable)
	if _, err := uc.Stats(context.Background(), true, "u"); !errors.Is(err, domain.ErrEngineUnreachable) {
		t.Fatalf("%v", err)
	}
	if _, err := uc.History(context.Background(), true, "u", 1); !errors.Is(err, domain.ErrEngineUnreachable) {
		t.Fatalf("%v", err)
	}
	if len(logs.All()) != 1 {
		t.Fatalf("un fallo no se anota como consulta: %d", len(logs.All()))
	}
}
