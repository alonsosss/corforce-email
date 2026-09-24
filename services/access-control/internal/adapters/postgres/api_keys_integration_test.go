//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/google/uuid"
)

// Las claves de API contra Postgres real con las migraciones del registro aplicadas dos veces:
// el catalogo que admite una clave, el alta con su evento en la outbox en la misma transaccion,
// la busqueda por prefijo, la cuenta de vigentes, el ultimo uso espaciado y la revocacion unica.
func TestClavesDeAPI(t *testing.T) {
	ctx := context.Background()
	pool := registryDB(t)
	repo := NewAPIKeyRepo(pool)
	tenant, other, owner := uuid.New(), uuid.New(), uuid.New()
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = pool.Exec(cctx, `DELETE FROM access_control.api_keys WHERE tenant_id = ANY($1)`, []uuid.UUID{tenant, other})
		_, _ = pool.Exec(cctx, `DELETE FROM platform.event_outbox WHERE tenant_id = ANY($1)`, []uuid.UUID{tenant, other})
	})

	grantable, err := repo.GrantablePermissions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range grantable {
		got[p.Module+":"+p.Resource+":"+p.Action] = true
	}
	if len(got) != 2 || !got["transactional:messages:create"] || !got["transactional:messages:read"] {
		t.Fatalf("catalogo de claves: %v", got)
	}
	var send *domain.Permission
	for _, p := range grantable {
		if p.Action == "create" {
			send = p
		}
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	expires := now.Add(24 * time.Hour)
	key := &domain.APIKey{
		ID: uuid.New(), TenantID: tenant, Name: "Tienda", Prefix: "it" + uuid.NewString()[:10], SecretHash: []byte{1, 2, 3},
		HashKeyID: "k1", CreatedBy: owner, Scopes: []domain.Permission{*send}, ExpiresAt: &expires, CreatedAt: now,
	}
	event := domain.APIKeyEvent{Type: domain.APIKeyEventCreated, TenantID: tenant, ActorID: owner, KeyID: key.ID,
		Name: key.Name, Prefix: key.Prefix, Scopes: key.Scopes, ExpiresAt: key.ExpiresAt, At: now}
	if err := repo.Create(ctx, key, event); err != nil {
		t.Fatal(err)
	}
	outbox := func(subject string) int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.event_outbox WHERE tenant_id = $1 AND subject = $2
			AND payload->'data'->>'api_key_id' = $3 AND NOT (payload::text LIKE '%secret%')`, tenant, subject, key.ID.String()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if outbox(SubjectAPIKeyCreated) != 1 {
		t.Fatal("el alta deja su evento en la outbox")
	}
	// Un prefijo repetido no entra: es la clave de busqueda de toda la plataforma.
	dup := *key
	dup.ID, dup.TenantID = uuid.New(), other
	if err := repo.Create(ctx, &dup, event); err == nil {
		t.Fatal("prefijo repetido admitido")
	}

	found, err := repo.GetByPrefix(ctx, key.Prefix)
	if err != nil || found.ID != key.ID || len(found.Scopes) != 1 || found.Scopes[0].ID != send.ID || !found.ExpiresAt.Equal(expires) {
		t.Fatalf("por prefijo: %+v %v", found, err)
	}
	if _, err := repo.GetByPrefix(ctx, "noexiste0000"); !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Fatalf("inexistente: %v", err)
	}
	if list, err := repo.List(ctx, tenant); err != nil || len(list) != 1 || len(list[0].Scopes) != 1 {
		t.Fatalf("lista: %v %v", list, err)
	}
	if n, err := repo.CountActive(ctx, tenant, now); err != nil || n != 1 {
		t.Fatalf("vigentes: %d %v", n, err)
	}
	if n, _ := repo.CountActive(ctx, tenant, expires.Add(time.Second)); n != 0 {
		t.Fatal("una caducada no cuenta")
	}

	if err := repo.TouchUsage(ctx, key.ID, now, "198.51.100.7", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := repo.TouchUsage(ctx, key.ID, now.Add(10*time.Second), "203.0.113.9", time.Minute); err != nil {
		t.Fatal(err)
	}
	found, _ = repo.Get(ctx, tenant, key.ID)
	if found.LastUsedAt == nil || !found.LastUsedAt.Equal(now) || found.LastUsedIP != "198.51.100.7" {
		t.Fatalf("el ultimo uso se espacia: %v %q", found.LastUsedAt, found.LastUsedIP)
	}
	if err := repo.Rehash(ctx, key.ID, []byte{9}, "k2"); err != nil {
		t.Fatal(err)
	}
	if found, _ = repo.Get(ctx, tenant, key.ID); found.HashKeyID != "k2" {
		t.Fatal("rehash")
	}

	if _, err := repo.Revoke(ctx, other, key.ID, owner, now, nil); !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Fatalf("de otra empresa: %v", err)
	}
	mk := func(k *domain.APIKey) domain.APIKeyEvent {
		return domain.APIKeyEvent{Type: domain.APIKeyEventRevoked, TenantID: k.TenantID, ActorID: owner, KeyID: k.ID, At: now}
	}
	revoked, err := repo.Revoke(ctx, tenant, key.ID, owner, now, mk)
	if err != nil || revoked.RevokedAt == nil || *revoked.RevokedBy != owner || len(revoked.Scopes) != 1 {
		t.Fatalf("revocada: %+v %v", revoked, err)
	}
	if _, err := repo.Revoke(ctx, tenant, key.ID, owner, now, mk); !errors.Is(err, domain.ErrAPIKeyRevoked) {
		t.Fatalf("dos veces: %v", err)
	}
	if outbox(SubjectAPIKeyRevoked) != 1 {
		t.Fatal("una sola revocacion en la outbox")
	}
	if n, _ := repo.CountActive(ctx, tenant, now); n != 0 {
		t.Fatal("una revocada no cuenta")
	}
}
