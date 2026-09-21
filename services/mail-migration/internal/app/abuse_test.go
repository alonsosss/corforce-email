package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/google/uuid"
)

// El ejecutor sale a Internet con la IP de la plataforma hacia el servidor que elige el usuario: sin
// topes, una empresa lanzaria trabajos en cadena (uno por intento) para probar contrasenas de una
// cuenta ajena o para sondear servidores IMAP de Internet.

func TestCrearAcotaLosTrabajosPorEmpresaYDia(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.MaxActivePerTenant = 100; c.MaxJobsPerDay = 3 })
	for i := 0; i < 3; i++ {
		if _, err := f.uc.Create(context.Background(), f.tenant, f.actor, validInput()); err != nil {
			t.Fatalf("alta %d: %v", i+1, err)
		}
	}
	if _, err := f.uc.Create(context.Background(), f.tenant, f.actor, validInput()); !errors.Is(err, domain.ErrTenantRateLimited) {
		t.Fatalf("el cuarto trabajo del dia debe rechazarse: %v", err)
	}
	if _, err := f.uc.Create(context.Background(), uuid.New(), f.actor, validInput()); err != nil {
		t.Fatalf("otra empresa tiene su propio tope: %v", err)
	}
	f.now = f.now.Add(24*time.Hour + time.Minute)
	if _, err := f.uc.Create(context.Background(), f.tenant, f.actor, validInput()); err != nil {
		t.Fatalf("pasadas 24 horas el tope se libera: %v", err)
	}
}

func TestCrearFrenaLaFuerzaBrutaContraUnaCuentaDeOrigen(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.MaxActivePerTenant = 100; c.MaxAuthFailuresPerHour = 2 })
	rejected := func(host, user string, at time.Time) {
		fin := at
		f.repo.Jobs[uuid.New()] = &domain.Job{
			ID: uuid.New(), TenantID: f.tenant, SourceHost: host, SourceUsername: user, Status: domain.StatusFailed,
			CreatedAt: at, FinishedAt: &fin, LastError: &domain.JobError{Code: domain.CodeSourceAuthFailed},
		}
	}
	rejected("imap.origen.example", "Ana@origen.example", f.now.Add(-10*time.Minute))
	rejected("imap.origen.example", "ana@origen.example", f.now.Add(-5*time.Minute))

	if _, err := f.uc.Create(context.Background(), f.tenant, f.actor, validInput()); !errors.Is(err, domain.ErrSourceAuthCooldown) {
		t.Fatalf("dos rechazos de la misma cuenta en la ultima hora: %v", err)
	}
	other := validInput()
	other.Source.Username = "luis@origen.example"
	if _, err := f.uc.Create(context.Background(), f.tenant, f.actor, other); err != nil {
		t.Fatalf("otra cuenta del mismo servidor no esta frenada: %v", err)
	}
	f.now = f.now.Add(56 * time.Minute)
	if _, err := f.uc.Create(context.Background(), f.tenant, f.actor, validInput()); err != nil {
		t.Fatalf("pasada la hora de los rechazos se puede reintentar: %v", err)
	}
}

func TestReclamarNoEntregaElCifradoPegadoDeOtroTrabajo(t *testing.T) {
	f := newFixture(t, nil)
	victim, err := f.uc.Create(context.Background(), f.tenant, f.actor, validInput())
	if err != nil {
		t.Fatal(err)
	}
	mine := validInput()
	mine.Source.Password = "contrasena-del-atacante"
	attacker, err := f.uc.Create(context.Background(), f.tenant, f.actor, mine)
	if err != nil {
		t.Fatal(err)
	}
	f.repo.Jobs[attacker.ID].SourcePasswordEnc = f.repo.Jobs[victim.ID].SourcePasswordEnc

	for i := 0; i < 2; i++ {
		claimed, err := f.uc.Claim(context.Background(), "runner-"+uuid.NewString())
		if err != nil {
			t.Fatal(err)
		}
		if claimed != nil && claimed.JobID == attacker.ID {
			t.Fatalf("el trabajo con el cifrado pegado recibio %q", claimed.SourcePassword)
		}
	}
	if got := f.repo.Jobs[attacker.ID]; got.Status != domain.StatusFailed || got.LastError == nil || got.LastError.Code != domain.CodeCredentialUnreadable {
		t.Fatalf("estado: %+v", got)
	}
}

func TestCrearSoloCuentaRechazosDeAutenticacion(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.MaxActivePerTenant = 100; c.MaxAuthFailuresPerHour = 1 })
	fin := f.now.Add(-time.Minute)
	f.repo.Jobs[uuid.New()] = &domain.Job{
		ID: uuid.New(), TenantID: f.tenant, SourceHost: "imap.origen.example", SourceUsername: "ana@origen.example", Status: domain.StatusFailed,
		CreatedAt: fin, FinishedAt: &fin, LastError: &domain.JobError{Code: domain.CodeSourceUnreachable},
	}
	if _, err := f.uc.Create(context.Background(), f.tenant, f.actor, validInput()); err != nil {
		t.Fatalf("un servidor caido no es un intento de adivinar la contrasena: %v", err)
	}
}
