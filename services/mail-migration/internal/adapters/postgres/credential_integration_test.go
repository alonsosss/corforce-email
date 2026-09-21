//go:build integration

package postgres

// Credencial de destino por trabajo contra un Postgres real: solo vale con el trabajo en curso, el lease
// vigente y sin cancelacion pedida; todo cierre, cancelacion, vencimiento y borrado la revoca, y la base
// misma impide que un trabajo que no esta en curso la conserve.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/ports"
	"github.com/google/uuid"
)

func hashOf(seed byte) []byte {
	sum := sha256.Sum256(bytes.Repeat([]byte{seed}, 32))
	return sum[:]
}

func claimWithHash(t *testing.T, ctx context.Context, repo *Repository, tenant uuid.UUID, hash []byte) *domain.Job {
	t.Helper()
	j := newJob(tenant)
	if err := insert(t, ctx, repo, j, 100); err != nil {
		t.Fatal(err)
	}
	p := claimParams("runner-1")
	p.DestinationCredentialHash = hash
	c, err := repo.Claim(ctx, tenant, p)
	if err != nil || c == nil {
		t.Fatalf("claim: %v %v", c, err)
	}
	return c
}

func storedHash(t *testing.T, ctx context.Context, r *Repository, id uuid.UUID) []byte {
	t.Helper()
	var h []byte
	if err := r.pool.QueryRow(ctx, `SELECT destination_credential_hash FROM mail_migration.jobs WHERE id = $1`, id).Scan(&h); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestLaCredencialDeDestinoSoloValeConElTrabajoVivo(t *testing.T) {
	ctx, repo, _ := setup(t)
	tenant := uuid.New()
	c := claimWithHash(t, ctx, repo, tenant, hashOf(1))

	target, err := repo.DestinationTarget(ctx, tenant, c.ID, now.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if target.MailboxID != c.MailboxID || target.MailboxUsername != c.MailboxUsername || !bytes.Equal(target.Hash, hashOf(1)) {
		t.Fatalf("destino: %+v", target)
	}

	if _, err := repo.DestinationTarget(ctx, uuid.New(), c.ID, now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("otra empresa con el mismo id de trabajo: %v", err)
	}
	if _, err := repo.DestinationTarget(ctx, tenant, uuid.New(), now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("trabajo inexistente: %v", err)
	}
	if _, err := repo.DestinationTarget(ctx, tenant, c.ID, now.Add(91*time.Second)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("con el lease vencido no debe abrir: %v", err)
	}
	if _, err := repo.DestinationTarget(ctx, tenant, c.ID, now.Add(90*time.Second)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("el lease vence en su instante exacto: %v", err)
	}
}

func TestSinCredencialEmitidaNoHayDestino(t *testing.T) {
	ctx, repo, _ := setup(t)
	tenant := uuid.New()
	c := claimWithHash(t, ctx, repo, tenant, nil)
	if storedHash(t, ctx, repo, c.ID) != nil {
		t.Fatal("guardo un hash sin que se emitiera")
	}
	if _, err := repo.DestinationTarget(ctx, tenant, c.ID, now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("un trabajo sin credencial abre: %v", err)
	}
}

func TestPedirLaCancelacionRevocaLaCredencialAlInstante(t *testing.T) {
	ctx, repo, _ := setup(t)
	tenant := uuid.New()
	c := claimWithHash(t, ctx, repo, tenant, hashOf(2))
	if _, err := repo.RequestCancel(ctx, tenant, c.ID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if storedHash(t, ctx, repo, c.ID) != nil {
		t.Fatal("la cancelacion no borro la credencial")
	}
	if _, err := repo.DestinationTarget(ctx, tenant, c.ID, now.Add(2*time.Second)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cancelado pero abre: %v", err)
	}
}

func TestCerrarElTrabajoRevocaLaCredencial(t *testing.T) {
	ctx, repo, _ := setup(t)
	tenant := uuid.New()
	for _, status := range []domain.Status{domain.StatusSucceeded, domain.StatusFailed, domain.StatusCancelled} {
		c := claimWithHash(t, ctx, repo, tenant, hashOf(3))
		var jobErr *domain.JobError
		if status == domain.StatusFailed {
			jobErr = &domain.JobError{Code: domain.CodeImapsyncFailed, Message: "fallo"}
		}
		if _, err := repo.Finish(ctx, tenant, c.ID, ports.FinishParams{LeaseID: *c.LeaseID, Status: status, Error: jobErr, Now: now}); err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		if storedHash(t, ctx, repo, c.ID) != nil {
			t.Fatalf("%s: la credencial sobrevive al cierre", status)
		}
		if _, err := repo.DestinationTarget(ctx, tenant, c.ID, now); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("%s: abre tras cerrar: %v", status, err)
		}
	}
}

func TestElVencimientoDelLeaseRevocaLaCredencialAlCerrarElTrabajoPerdido(t *testing.T) {
	ctx, repo, _ := setup(t)
	tenant := uuid.New()
	j := newJob(tenant)
	if err := insert(t, ctx, repo, j, 100); err != nil {
		t.Fatal(err)
	}
	p := claimParams("runner-1")
	p.MaxAttempts = 1
	p.DestinationCredentialHash = hashOf(4)
	if _, err := repo.Claim(ctx, tenant, p); err != nil {
		t.Fatal(err)
	}
	expired, err := repo.ExpireLost(ctx, tenant, now.Add(10*time.Minute), 1)
	if err != nil || len(expired) != 1 {
		t.Fatalf("ExpireLost: %v %+v", err, expired)
	}
	if storedHash(t, ctx, repo, j.ID) != nil {
		t.Fatal("la credencial sobrevive al trabajo perdido")
	}
}

func TestUnNuevoReclamoReemplazaLaCredencial(t *testing.T) {
	ctx, repo, _ := setup(t)
	tenant := uuid.New()
	first := claimWithHash(t, ctx, repo, tenant, hashOf(5))

	second := claimParams("runner-2")
	second.Now = now.Add(5 * time.Minute)
	second.LeaseUntil = now.Add(6 * time.Minute)
	second.DestinationCredentialHash = hashOf(6)
	c, err := repo.Claim(ctx, tenant, second)
	if err != nil || c == nil || c.ID != first.ID || c.Attempt != 2 {
		t.Fatalf("reclamo del trabajo perdido: %+v %v", c, err)
	}
	target, err := repo.DestinationTarget(ctx, tenant, c.ID, second.Now.Add(time.Second))
	if err != nil || !bytes.Equal(target.Hash, hashOf(6)) {
		t.Fatalf("destino tras el reclamo: %+v %v", target, err)
	}
	if bytes.Equal(target.Hash, hashOf(5)) {
		t.Fatal("sigue la credencial del ejecutor anterior")
	}
}

func TestBorrarElBuzonBorraLaCredencial(t *testing.T) {
	ctx, repo, _ := setup(t)
	tenant := uuid.New()
	c := claimWithHash(t, ctx, repo, tenant, hashOf(7))
	if _, err := repo.DeleteByMailbox(ctx, tenant, c.MailboxID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DestinationTarget(ctx, tenant, c.ID, now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("buzon borrado pero abre: %v", err)
	}
}

func TestLaBaseImpideUnaCredencialFueraDeUnTrabajoEnCurso(t *testing.T) {
	ctx, repo, pool := setup(t)
	tenant := uuid.New()

	pending := newJob(tenant)
	if err := insert(t, ctx, repo, pending, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE mail_migration.jobs SET destination_credential_hash = $2 WHERE id = $1`, pending.ID, hashOf(8)); err == nil {
		t.Fatal("la base admitio una credencial en un trabajo pendiente")
	}

	c := claimWithHash(t, ctx, repo, tenant, hashOf(9))
	if _, err := pool.Exec(ctx, `UPDATE mail_migration.jobs SET destination_credential_hash = $2 WHERE id = $1`, c.ID, []byte("corto")); err == nil {
		t.Fatal("la base admitio un hash que no es SHA-256")
	}
	if _, err := pool.Exec(ctx,
		`UPDATE mail_migration.jobs SET status = 'failed', finished_at = now(), source_password_enc = NULL, lease_id = NULL, lease_expires_at = NULL WHERE id = $1`, c.ID); err == nil {
		t.Fatal("la base admitio un trabajo fallido que conserva la credencial de destino")
	}
}
