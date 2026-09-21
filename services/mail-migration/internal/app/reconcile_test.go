package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/google/uuid"
)

func seedJob(t *testing.T, f *fixture, mailbox uuid.UUID, createdAt time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	f.repo.Jobs[id] = &domain.Job{
		ID: id, TenantID: f.tenant, MailboxID: mailbox, MailboxUsername: "ana@acme.test", Status: domain.StatusSucceeded,
		CreatedAt: createdAt,
	}
	return id
}

func TestLosBuzonesConTrabajosDentroDeLaGraciaNoSeListan(t *testing.T) {
	f := newFixture(t, nil)
	old, recent := uuid.New(), uuid.New()
	seedJob(t, f, old, f.now.Add(-48*time.Hour))
	seedJob(t, f, recent, f.now.Add(-time.Hour))

	got, err := f.uc.StaleMailboxes(context.Background(), f.tenant, f.now.Add(-24*time.Hour), uuid.Nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != old {
		t.Fatalf("estancados: %v (solo el buzon con un trabajo mas viejo que la gracia)", got)
	}
}

func TestUnBuzonEsEstancadoSiSuTrabajoMasAntiguoLoEsAunqueTengaOtroReciente(t *testing.T) {
	f := newFixture(t, nil)
	mailbox := uuid.New()
	seedJob(t, f, mailbox, f.now.Add(-72*time.Hour))
	seedJob(t, f, mailbox, f.now.Add(-time.Minute))
	got, _ := f.uc.StaleMailboxes(context.Background(), f.tenant, f.now.Add(-24*time.Hour), uuid.Nil, 100)
	if len(got) != 1 || got[0] != mailbox {
		t.Fatalf("estancados: %v", got)
	}
}

func TestLaConciliacionSoloVeLosBuzonesDeSuEmpresa(t *testing.T) {
	f := newFixture(t, nil)
	seedJob(t, f, uuid.New(), f.now.Add(-48*time.Hour))
	other := uuid.New()
	got, err := f.uc.StaleMailboxes(context.Background(), other, f.now, uuid.Nil, 100)
	if err != nil || len(got) != 0 {
		t.Fatalf("otra empresa: %v %v", got, err)
	}
}

func TestLaConciliacionPaginaEnOrdenSinRepetir(t *testing.T) {
	f := newFixture(t, nil)
	for i := 0; i < 5; i++ {
		seedJob(t, f, uuid.New(), f.now.Add(-48*time.Hour))
	}
	seen := map[uuid.UUID]bool{}
	after := uuid.Nil
	for {
		page, err := f.uc.StaleMailboxes(context.Background(), f.tenant, f.now, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, id := range page {
			if seen[id] {
				t.Fatalf("repetido %s", id)
			}
			seen[id] = true
		}
		after = page[len(page)-1]
	}
	if len(seen) != 5 {
		t.Fatalf("recorrio %d de 5", len(seen))
	}
}

func TestLaConciliacionRechazaEmpresaNulaYEmpresaDesconocida(t *testing.T) {
	f := newFixture(t, nil)
	if _, err := f.uc.StaleMailboxes(context.Background(), uuid.Nil, f.now, uuid.Nil, 10); !errors.Is(err, domain.ErrInvalidMailbox) {
		t.Fatalf("empresa nula: %v", err)
	}
	f.tenants.ForErr = domain.ErrTenantUnknown
	if _, err := f.uc.StaleMailboxes(context.Background(), f.tenant, f.now, uuid.Nil, 10); !errors.Is(err, domain.ErrTenantUnknown) {
		t.Fatalf("empresa desconocida: %v", err)
	}
}

func TestRetirarUnBuzonConciliadoAnunciaLaCancelacionDeLosActivosComoElConsumidor(t *testing.T) {
	f := newFixture(t, nil)
	mailbox := uuid.New()
	active := seedJob(t, f, mailbox, f.now.Add(-48*time.Hour))
	f.repo.Jobs[active].Status = domain.StatusRunning
	seedJob(t, f, mailbox, f.now.Add(-72*time.Hour))
	other := seedJob(t, f, uuid.New(), f.now.Add(-72*time.Hour))

	removed, err := f.uc.PurgeMailbox(context.Background(), f.tenant, mailbox)
	if err != nil || removed != 2 {
		t.Fatalf("retirados %d: %v", removed, err)
	}
	if _, ok := f.repo.Jobs[other]; !ok {
		t.Fatal("retiro los trabajos de otro buzon")
	}
	if len(f.events.Log) != 1 || f.events.Log[0] != "finished:cancelled" {
		t.Fatalf("eventos: %v", f.events.Log)
	}
}
