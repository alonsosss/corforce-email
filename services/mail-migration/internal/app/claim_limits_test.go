package app

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Un ejecutor honesto hace un trabajo a la vez. Uno comprometido (imapsync habla con servidores
// ajenos) que reclamara todo lo pendiente se llevaria las contrasenas de origen de muchas empresas de
// golpe: el servicio no le entrega un segundo trabajo mientras conserve el primero, ni mas de
// MaxRunningJobs a la vez entre todos los ejecutores, cambie el identificador que cambie.

func pendingJobs(t *testing.T, f *fixture, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := f.uc.Create(context.Background(), f.tenant, f.actor, validInput()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReclamarNoEntregaUnSegundoTrabajoAlMismoEjecutor(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.MaxActivePerTenant = 10; c.MaxRunningJobs = 10 })
	pendingJobs(t, f, 3)
	first, err := f.uc.Claim(context.Background(), "runner-1")
	if err != nil || first == nil {
		t.Fatalf("primer reclamo: %v %v", first, err)
	}
	if second, err := f.uc.Claim(context.Background(), "runner-1"); err != nil || second != nil {
		t.Fatalf("el mismo ejecutor no puede tener dos trabajos: %+v %v", second, err)
	}
	if other, err := f.uc.Claim(context.Background(), "runner-2"); err != nil || other == nil {
		t.Fatalf("otro ejecutor si puede tomar uno: %+v %v", other, err)
	}

	if _, err := f.uc.Heartbeat(context.Background(), f.tenant, first.JobID, HeartbeatInput{LeaseID: first.LeaseID, Phase: "initial"}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	if again, err := f.uc.Claim(context.Background(), "runner-1"); err != nil || again != nil {
		t.Fatalf("con el latido al dia sigue teniendo su trabajo: %+v %v", again, err)
	}

	if _, err := f.uc.Complete(context.Background(), f.tenant, first.JobID, CompleteInput{LeaseID: first.LeaseID, Outcome: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if next, err := f.uc.Claim(context.Background(), "runner-1"); err != nil || next == nil {
		t.Fatalf("tras cerrar el trabajo puede tomar otro: %+v %v", next, err)
	}
}

func TestReclamarSueltaElTrabajoDeUnEjecutorQueDejoDeDarLatido(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.MaxActivePerTenant = 10; c.MaxRunningJobs = 10 })
	pendingJobs(t, f, 2)
	if first, err := f.uc.Claim(context.Background(), "runner-1"); err != nil || first == nil {
		t.Fatalf("primer reclamo: %v %v", first, err)
	}
	f.now = f.now.Add(f.uc.cfg.Lease + time.Second)
	if next, err := f.uc.Claim(context.Background(), "runner-1"); err != nil || next == nil {
		t.Fatalf("vencido el lease el ejecutor (reiniciado con el mismo nombre) puede volver a reclamar: %+v %v", next, err)
	}
}

func TestReclamarAcotaLosTrabajosEnCursoEntreTodosLosEjecutores(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.MaxActivePerTenant = 20; c.MaxRunningJobs = 2 })
	pendingJobs(t, f, 6)
	got := 0
	for i := 0; i < 6; i++ {
		c, err := f.uc.Claim(context.Background(), "runner-"+uuid.NewString())
		if err != nil {
			t.Fatal(err)
		}
		if c != nil {
			got++
		}
	}
	if got != 2 {
		t.Fatalf("seis ejecutores con nombres distintos reclamaron %d trabajos, tope 2", got)
	}
}
