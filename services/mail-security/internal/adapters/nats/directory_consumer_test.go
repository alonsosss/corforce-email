package nats

import (
	"context"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Un evento del directorio sobre un dominio que deja de estar activo (desactivado o borrado) le
// quita las claves DKIM de los motores; uno sobre un dominio activo no las toca. El consumidor
// lee el estado real, asi que reentregar el evento no cambia nada.
func TestUnDominioQueDejaDeEstarActivoPierdeSusClavesDKIM(t *testing.T) {
	ctx := context.Background()
	dir, store, policy := apptest.NewDirectory(), apptest.NewStore(), apptest.NewPolicyReader()
	sync := app.NewRedisSync(store, dir, policy, zap.NewNop())
	dkim := app.NewDKIMUseCase(app.DKIMDeps{Directory: dir, Lock: &apptest.DKIMLock{}, Sync: sync, Tenants: &apptest.Tenants{}, Logger: zap.NewNop()})
	c := NewDirectoryConsumer(nil, sync, dkim, policy, func(ctx context.Context) context.Context { return ctx }, zap.NewNop())

	tenant := uuid.New()
	dir.Domains["acme.com"] = tenant
	if err := sync.SyncDKIM(ctx, domain.DKIMKey{Domain: "acme.com", Selector: "s1", PrivateKeyPEM: "pem"}); err != nil {
		t.Fatal(err)
	}
	entregar := func(subject string) {
		t.Helper()
		acked := false
		c.handle(events.Event{Type: subject, Data: map[string]any{"domain": "acme.com"}}, func() { acked = true })
		if !acked {
			t.Fatalf("%s sin confirmar", subject)
		}
	}

	entregar("mail.domain.updated")
	if _, ok := store.Hashes[domain.RedisDKIMSelectors]["acme.com"]; !ok {
		t.Fatal("un dominio activo conserva sus claves")
	}

	delete(dir.Domains, "acme.com")
	dir.InactiveDomains["acme.com"] = tenant
	for i := 0; i < 2; i++ {
		entregar("mail.domain.activated")
	}
	if len(store.Hashes[domain.RedisDKIMPrivKeys]) != 0 || len(store.Hashes[domain.RedisDKIMSelectors]) != 0 {
		t.Fatalf("un dominio desactivado se queda sin claves: %v / %v", store.Hashes[domain.RedisDKIMPrivKeys], store.Hashes[domain.RedisDKIMSelectors])
	}
	if _, ok := store.Hashes[domain.RedisDomainMap]["acme.com"]; ok {
		t.Fatal("y fuera de DOMAIN_MAP")
	}
}
