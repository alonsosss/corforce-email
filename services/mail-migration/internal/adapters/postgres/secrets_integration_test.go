//go:build integration

package postgres

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/crypto"
	outboxadapter "github.com/alonsosss/corforce-email/services/mail-migration/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/ports"
	"github.com/google/uuid"
)

func rotationRing(t *testing.T, active, old string) *crypto.KeyRing {
	t.Helper()
	t.Setenv("MIGRATION_ROTATION_KEY", active)
	t.Setenv("MIGRATION_ROTATION_KEYS_OLD", old)
	kr, err := crypto.LoadKeyRing("MIGRATION_ROTATION_KEY", "MIGRATION_ROTATION_KEYS_OLD")
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

// La contrasena de origen re-cifrada con los datos autenticados de SealedColumns (empresa y trabajo)
// la entrega el reclamo del ejecutor con la llave nueva sola; una segunda pasada no toca nada.
func TestLaContrasenaDeOrigenSeRecifraBajoLaLlaveActiva(t *testing.T) {
	ctx, repo, _ := setup(t)
	oldKey, newKey := strings.Repeat("c3", 32), strings.Repeat("d4", 32)
	tenant := uuid.New()
	const password = "contrasena-de-origen-a-rotar"
	useCase := func(ring *crypto.KeyRing) *app.UseCase {
		return app.New(app.Deps{
			Repo: repo, Tx: repo, Mailboxes: &apptest.Mailboxes{Ref: ports.MailboxRef{Username: "ana@acme.test", Active: true}},
			Resolver: &apptest.Resolver{Addrs: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}, Cipher: ring,
			Tenants: &apptest.Tenants{IDs: []uuid.UUID{tenant}}, Events: outboxadapter.NewPublisher(repo.pool),
			Config: app.Config{RunnerConfigured: true, MaxActivePerTenant: 2, Lease: 90 * time.Second, MaxAttempts: 3, SweepInterval: time.Second,
				Source: domain.SourcePolicy{Ports: []int{143, 993}}},
		})
	}
	job, err := useCase(rotationRing(t, oldKey, "")).Create(ctx, tenant, uuid.New(), app.CreateInput{MailboxID: uuid.New(), Source: domain.Source{
		Host: "imap.origen.example", Port: 993, TLS: domain.TLSImplicit, Username: "ana@origen.example", Password: password}})
	if err != nil {
		t.Fatal(err)
	}
	before := storedPassword(t, ctx, repo, job.ID)

	targets := SealedColumns()
	if len(targets) != 1 || targets[0].AAD == nil {
		t.Fatalf("columnas: %+v", targets)
	}
	target := targets[0]
	aad := func(key uuid.UUID) []byte { return target.AAD(tenant, key) }
	rotating := rotationRing(t, newKey, oldKey)
	rep, err := crypto.RotateStore(ctx, rotating, target.Column, uuid.Nil, 50, aad)
	if err != nil || rep.Rotated < 1 {
		t.Fatalf("rotacion: %+v %v", rep, err)
	}
	if after := storedPassword(t, ctx, repo, job.ID); string(after) == string(before) {
		t.Fatal("la contrasena de origen no se re-cifro")
	}
	if again, err := crypto.RotateStore(ctx, rotating, target.Column, uuid.Nil, 50, aad); err != nil || again.Rotated != 0 {
		t.Fatalf("segunda pasada: %+v %v", again, err)
	}

	claimed, err := useCase(rotationRing(t, newKey, "")).Claim(ctx, "runner-"+uuid.NewString())
	if err != nil || claimed == nil || claimed.JobID != job.ID || claimed.SourcePassword != password {
		t.Fatalf("reclamo con la llave nueva sola: %+v %v", claimed, err)
	}
}
