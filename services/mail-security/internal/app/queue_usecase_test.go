package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func newQueueUC() (*QueueUseCase, *apptest.Queue, *observer.ObservedLogs) {
	engine := apptest.NewQueue()
	core, logs := observer.New(zap.InfoLevel)
	return NewQueueUseCase(engine, zap.New(core)), engine, logs
}

func TestSoloElOperadorDeLaPlataformaVeYCambiaLaCola(t *testing.T) {
	uc, engine, _ := newQueueUC()
	ctx := context.Background()
	if _, err := uc.List(ctx, false, 10); !errors.Is(err, domain.ErrPlatformOnly) {
		t.Fatalf("List: %v", err)
	}
	for _, a := range []domain.QueueAction{domain.QueueRetry, domain.QueueHold, domain.QueueUnhold, domain.QueueDelete} {
		if err := uc.Apply(ctx, false, "u", a, "ABCDEF1234"); !errors.Is(err, domain.ErrPlatformOnly) {
			t.Fatalf("Apply %s: %v", a, err)
		}
	}
	if err := uc.Flush(ctx, false, "u"); !errors.Is(err, domain.ErrPlatformOnly) {
		t.Fatalf("Flush: %v", err)
	}
	if engine.ListLimit != 0 || len(engine.Applied) != 0 || engine.Flushed != 0 {
		t.Fatalf("un administrador de empresa no debe llegar al motor: %+v", engine)
	}
}

func TestElLimiteDeLaListaSeAcotaDesdeElDominio(t *testing.T) {
	uc, engine, _ := newQueueUC()
	for limit, want := range map[int]int{0: domain.DefaultQueueListLimit, -5: domain.DefaultQueueListLimit, 30: 30, 100000: domain.MaxQueueListLimit} {
		if _, err := uc.List(context.Background(), true, limit); err != nil {
			t.Fatal(err)
		}
		if engine.ListLimit != want {
			t.Errorf("limit %d llego como %d, se esperaba %d", limit, engine.ListLimit, want)
		}
	}
}

func TestUnaAccionValidaLlegaAlMotorYQuedaRegistradaConSuAutor(t *testing.T) {
	uc, engine, logs := newQueueUC()
	if err := uc.Apply(context.Background(), true, "user-42", domain.QueueDelete, "4Xy1Zk2hSBzXyZ"); err != nil {
		t.Fatal(err)
	}
	if len(engine.Applied) != 1 || engine.Applied[0] != "delete 4Xy1Zk2hSBzXyZ" {
		t.Fatalf("aplicadas: %v", engine.Applied)
	}
	entry := logs.All()
	if len(entry) != 1 {
		t.Fatalf("registros: %v", entry)
	}
	f := entry[0].ContextMap()
	if f["actor"] != "user-42" || f["queue_id"] != "4Xy1Zk2hSBzXyZ" || f["action"] != "delete" {
		t.Fatalf("el registro debe decir quien, que y sobre que: %v", f)
	}
}

func TestUnaEntradaInvalidaNoLlegaAlMotor(t *testing.T) {
	uc, engine, logs := newQueueUC()
	ctx := context.Background()
	var verr *domain.ValidationError
	for _, id := range []string{"", "ABC", "-d ALL", "ABC;reboot", strings.Repeat("A", 26), "ABCDE\n"} {
		if err := uc.Apply(ctx, true, "u", domain.QueueDelete, id); !errors.As(err, &verr) {
			t.Errorf("identificador %q: %v", id, err)
		}
	}
	if err := uc.Apply(ctx, true, "u", domain.QueueAction("super_delete"), "ABCDEF1234"); !errors.As(err, &verr) {
		t.Errorf("accion desconocida: %v", err)
	}
	if len(engine.Applied) != 0 || logs.Len() != 0 {
		t.Fatalf("no debe llegar nada al motor ni registrarse: %v", engine.Applied)
	}
}

func TestUnFalloDelMotorSeDevuelveYNoSeRegistraComoHecho(t *testing.T) {
	uc, engine, logs := newQueueUC()
	engine.Err = domain.ErrEngineUnreachable
	if err := uc.Apply(context.Background(), true, "u", domain.QueueHold, "ABCDEF1234"); !errors.Is(err, domain.ErrEngineUnreachable) {
		t.Fatal(err)
	}
	if err := uc.Flush(context.Background(), true, "u"); !errors.Is(err, domain.ErrEngineUnreachable) {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Fatalf("una accion que fallo no queda registrada como hecha: %v", logs.All())
	}
}

func TestSinAgenteConfiguradoLaColaEstaDesactivada(t *testing.T) {
	uc := NewQueueUseCase(nil, zap.NewNop())
	ctx := context.Background()
	if _, err := uc.List(ctx, true, 10); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("List: %v", err)
	}
	if err := uc.Apply(ctx, true, "u", domain.QueueRetry, "ABCDEF1234"); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("Apply: %v", err)
	}
	if err := uc.Flush(ctx, true, "u"); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("Flush: %v", err)
	}
}
