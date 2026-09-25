package keyrotation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func ring(t *testing.T, active, old string) *crypto.KeyRing {
	t.Helper()
	t.Setenv("KEYROTATION_TEST_KEY", active)
	t.Setenv("KEYROTATION_TEST_KEYS_OLD", old)
	kr, err := crypto.LoadKeyRing("KEYROTATION_TEST_KEY", "KEYROTATION_TEST_KEYS_OLD")
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

// Los nombres de la columna van al SQL tal cual: solo se admiten identificadores simples, y salen
// entrecomillados.
func TestNewColumnSoloAdmiteIdentificadoresSimples(t *testing.T) {
	c, err := NewColumn(nil, "domains.domains", "id", "dkim_private_key_enc")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.selectSQL, `FROM "domains"."domains"`) || !strings.Contains(c.updateSQL, `SET "dkim_private_key_enc" = $3`) {
		t.Fatalf("sql: %s | %s", c.selectSQL, c.updateSQL)
	}
	for _, bad := range [][3]string{
		{"domains", "id", "x"}, {"domains.domains;drop", "id", "x"}, {"a.b.c", "id", "x"},
		{"a.b", "id; --", "x"}, {"a.b", "id", `x"`}, {"A.b", "id", "x"}, {".b", "id", "x"},
	} {
		if _, err := NewColumn(nil, bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("%v aceptado", bad)
		}
	}
}

func TestSinBaseEnElContextoEsError(t *testing.T) {
	c := MustColumn(nil, "a.b", "id", "x")
	if _, err := c.SealedAfter(context.Background(), uuid.Nil, 1); err == nil {
		t.Fatal("sin pool ni base en el contexto")
	}
	if _, err := c.ReplaceSealed(context.Background(), uuid.Nil, nil, nil); err == nil {
		t.Fatal("sin pool ni base en el contexto")
	}
}

// Una empresa sin recorrer cuenta como pendiente: puede guardar datos bajo la llave vieja.
func TestLoPendienteIncluyeLasEmpresasSinRecorrer(t *testing.T) {
	r := Result{RotationReport: crypto.RotationReport{Pending: 2}, UnreachedTenants: []string{"a"}}
	if r.Outstanding() != 3 {
		t.Fatalf("pendientes %d", r.Outstanding())
	}
}

func TestReportRegistraLaLineaQueEsperaQuienRota(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	m := NewMetrics("keyrotation_test", "Datos de prueba")
	res := Result{RotationReport: crypto.RotationReport{Examined: 5, Rotated: 4, Pending: 1}}
	Report(context.Background(), "x.y", res, nil, m, zap.New(core))
	if got := logs.All(); len(got) != 1 || got[0].Message != "rotacion de MAIL_ENCRYPTION_KEY en x.y: 4 re-cifrados, 1 pendientes" {
		t.Fatalf("registro: %+v", got)
	}
	if testutil.ToFloat64(m.reencrypted) != 4 || testutil.ToFloat64(m.pending) != 1 {
		t.Fatal("metricas de la pasada completa")
	}
	// Interrumpida: suma lo re-cifrado, no toca los pendientes y avisa.
	Report(context.Background(), "x.y", Result{RotationReport: crypto.RotationReport{Rotated: 2}}, errors.New("caida"), m, zap.New(core))
	if testutil.ToFloat64(m.reencrypted) != 6 || testutil.ToFloat64(m.pending) != 1 || logs.All()[1].Level != zap.WarnLevel {
		t.Fatal("pasada interrumpida")
	}
}

// Sin llaves retiradas no hay nada que rotar: Run vuelve sin llamar a la pasada.
func TestRunSinLlavesRetiradasNoHaceNada(t *testing.T) {
	kr := ring(t, strings.Repeat("ab", 32), "")
	done := make(chan struct{})
	go func() {
		Run(context.Background(), kr, "x.y", func(context.Context) (Result, error) {
			t.Error("pasada sin llaves retiradas")
			return Result{}, nil
		}, nil, zap.NewNop())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run no volvio")
	}
}

// Con llaves retiradas pasa al arrancar y vuelve al cancelar.
func TestRunPasaAlArrancarYVuelveAlCancelar(t *testing.T) {
	kr := ring(t, strings.Repeat("ab", 32), strings.Repeat("cd", 32))
	ctx, cancel := context.WithCancel(context.Background())
	passes := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		Run(ctx, kr, "x.y", func(context.Context) (Result, error) {
			passes <- struct{}{}
			return Result{}, nil
		}, nil, zap.NewNop())
		close(done)
	}()
	select {
	case <-passes:
	case <-time.After(5 * time.Second):
		t.Fatal("sin pasada al arrancar")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run no volvio al cancelar")
	}
}
