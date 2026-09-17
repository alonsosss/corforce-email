//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/pkg/db"
	outboxadapter "github.com/alonsosss/corforce-email/services/domain-service/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
)

const tokenIntegracion = "cf_token_de_integracion_0123456789ABCDEF"

// La conexion con el proveedor DNS vive en la base de la empresa con el token cifrado: la columna
// no contiene el token en claro, se lee y reemplaza por empresa y desconectar la borra.
func TestRepositoryDNSProviderCifradoYAislado(t *testing.T) {
	ctx, repo := setup(t)
	t.Setenv("INTEGRACION_DNS_KEY", strings.Repeat("cd", 32))
	kr, err := crypto.LoadKeyRing("INTEGRACION_DNS_KEY", "INTEGRACION_DNS_KEY_OLD")
	if err != nil {
		t.Fatal(err)
	}
	tenantID, actor := uuid.New(), uuid.New()
	if _, err := repo.GetDNSProvider(ctx, tenantID, domain.DNSProviderCloudflare); !errors.Is(err, domain.ErrDNSProviderNotConnected) {
		t.Fatalf("sin conexion: %v", err)
	}
	enc, err := kr.Encrypt([]byte(tokenIntegracion))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	c := &domain.DNSProviderConnection{
		ID: uuid.New(), TenantID: tenantID, Provider: domain.DNSProviderCloudflare, TokenEnc: enc, TokenHint: "CDEF",
		ConnectedBy: actor, ConnectedAt: now,
	}
	c.SetZones([]domain.DNSZone{{ID: "a", Name: "acme.test"}, {ID: "b", Name: "otra.test"}}, now)
	if err := repo.SaveDNSProvider(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var claro bool
	if err := repo.pool.QueryRow(ctx,
		`SELECT position(convert_to($1, 'UTF8') IN api_token_enc) > 0 OR position(convert_to($2, 'UTF8') IN api_token_enc) > 0
 FROM domains.dns_providers WHERE tenant_id = $3`, tokenIntegracion, "0123456789ABCDEF", tenantID).Scan(&claro); err != nil {
		t.Fatal(err)
	}
	if claro {
		t.Fatal("la columna api_token_enc contiene el token en claro")
	}
	var columnas string
	if err := repo.pool.QueryRow(ctx,
		`SELECT string_agg(column_name, ',' ORDER BY ordinal_position) FROM information_schema.columns
 WHERE table_schema = 'domains' AND table_name = 'dns_providers'`).Scan(&columnas); err != nil {
		t.Fatal(err)
	}
	var enClaro int
	if err := repo.pool.QueryRow(ctx,
		`SELECT count(*) FROM domains.dns_providers p WHERE tenant_id = $1 AND (p.*)::text LIKE '%' || $2 || '%'`,
		tenantID, tokenIntegracion).Scan(&enClaro); err != nil {
		t.Fatal(err)
	}
	if enClaro != 0 {
		t.Fatalf("alguna columna (%s) guarda el token en claro", columnas)
	}

	got, err := repo.GetDNSProvider(ctx, tenantID, domain.DNSProviderCloudflare)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := kr.Decrypt(got.TokenEnc)
	if err != nil || string(plain) != tokenIntegracion {
		t.Fatalf("descifrar: %v", err)
	}
	if got.TokenHint != "CDEF" || got.ZonesVisible != 2 || strings.Join(got.Zones, ",") != "acme.test,otra.test" ||
		got.ConnectedBy != actor || !got.ConnectedAt.Equal(now) {
		t.Errorf("leida %+v", got)
	}
	if _, err := repo.GetDNSProvider(ctx, uuid.New(), domain.DNSProviderCloudflare); !errors.Is(err, domain.ErrDNSProviderNotConnected) {
		t.Errorf("otra empresa ve la conexion: %v", err)
	}

	// Reconectar reemplaza token y zonas en la misma fila.
	enc2, _ := kr.Encrypt([]byte("otro_token_de_integracion_0123456789"))
	c2 := &domain.DNSProviderConnection{ID: uuid.New(), TenantID: tenantID, Provider: domain.DNSProviderCloudflare, TokenEnc: enc2, TokenHint: "6789", ConnectedAt: now}
	c2.SetZones([]domain.DNSZone{{ID: "a", Name: "acme.test"}}, now)
	if err := repo.SaveDNSProvider(ctx, c2); err != nil {
		t.Fatal(err)
	}
	if c2.ID != c.ID {
		t.Errorf("reconectar crea otra fila: %s", c2.ID)
	}
	// Una validacion con el token anterior no pisa las zonas del nuevo.
	c.SetZones(nil, now.Add(time.Minute))
	if err := repo.UpdateDNSProviderZones(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.GetDNSProvider(ctx, tenantID, domain.DNSProviderCloudflare); got.ZonesVisible != 1 || got.TokenHint != "6789" || got.ConnectedBy != uuid.Nil {
		t.Errorf("tras reconectar %+v", got)
	}
	c2.SetZones([]domain.DNSZone{{ID: "a", Name: "acme.test"}, {ID: "c", Name: "nueva.test"}, {ID: "d", Name: "mas.test"}}, now.Add(time.Hour))
	if err := repo.UpdateDNSProviderZones(ctx, c2); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.GetDNSProvider(ctx, tenantID, domain.DNSProviderCloudflare); got.ZonesVisible != 3 || !got.LastValidatedAt.Equal(now.Add(time.Hour)) {
		t.Errorf("validacion %+v", got)
	}

	if _, err := repo.pool.Exec(ctx,
		`INSERT INTO domains.dns_providers (tenant_id, provider, api_token_enc) VALUES ($1, 'route53', '\x00')`, uuid.New()); err == nil {
		t.Error("la base admite un proveedor desconocido")
	}

	deleted, err := repo.DeleteDNSProvider(ctx, tenantID, domain.DNSProviderCloudflare)
	if err != nil || !deleted {
		t.Fatalf("Delete: %v %v", deleted, err)
	}
	var filas int
	_ = repo.pool.QueryRow(ctx, `SELECT count(*) FROM domains.dns_providers WHERE tenant_id = $1`, tenantID).Scan(&filas)
	if filas != 0 {
		t.Error("desconectar no borro el token")
	}
	if deleted, _ := repo.DeleteDNSProvider(ctx, tenantID, domain.DNSProviderCloudflare); deleted {
		t.Error("borrar dos veces")
	}
}

func TestRepositoryDNSModeYPublicacion(t *testing.T) {
	ctx, repo := setup(t)
	tenantID := uuid.New()
	d := sample(tenantID, "modo-"+uuid.NewString()[:8]+".test")
	if err := repo.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	other := sample(tenantID, "otro-"+uuid.NewString()[:8]+".test")
	if err := repo.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	ajeno := sample(uuid.New(), "ajeno-"+uuid.NewString()[:8]+".test")
	if err := repo.Create(ctx, ajeno); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetByID(ctx, tenantID, d.ID)
	if got.DNSMode != domain.DNSModeManual || got.DNSPublishedAt != nil {
		t.Fatalf("un dominio nace en manual: %s", got.DNSMode)
	}
	for _, x := range []*domain.Domain{d, other} {
		if err := repo.SetDNSMode(ctx, tenantID, x.ID, "cloudflare"); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.SetDNSMode(ctx, ajeno.TenantID, ajeno.ID, "cloudflare"); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetDNSMode(ctx, uuid.New(), d.ID, "manual"); !errors.Is(err, domain.ErrDomainNotFound) {
		t.Errorf("otra empresa cambia el modo: %v", err)
	}
	if err := repo.SetDNSMode(ctx, tenantID, d.ID, "route53"); err == nil {
		t.Error("la base admite un modo desconocido")
	}
	// Update no toca el modo: una verificacion con la fila de antes no lo devuelve a manual.
	stale := *got
	stale.Status = domain.StatusFailed
	if err := repo.Update(ctx, &stale); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.MarkDNSPublished(ctx, tenantID, d.ID, at); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetByID(ctx, tenantID, d.ID)
	if got.DNSMode != "cloudflare" || got.DNSPublishedAt == nil || !got.DNSPublishedAt.Equal(at) || got.Status != domain.StatusFailed {
		t.Errorf("modo %s publicado %v estado %s", got.DNSMode, got.DNSPublishedAt, got.Status)
	}
	n, err := repo.ResetDNSMode(ctx, tenantID, "cloudflare")
	if err != nil || n != 2 {
		t.Fatalf("ResetDNSMode: %d %v", n, err)
	}
	if got, _ := repo.GetByID(ctx, ajeno.TenantID, ajeno.ID); got.DNSMode != "cloudflare" {
		t.Error("desconectar en una empresa cambia los dominios de otra")
	}
}

// Los eventos de la publicacion automatica se encolan en la transaccion y no llevan el token.
func TestRepositoryDNSEventosEnLaTransaccion(t *testing.T) {
	ctx, repo := setup(t)
	q := &db.ContextPool{}
	events := outboxadapter.NewPublisher(q)
	tenantID, actor := uuid.New(), uuid.New()
	c := &domain.DNSProviderConnection{
		ID: uuid.New(), TenantID: tenantID, Provider: domain.DNSProviderCloudflare,
		TokenEnc: []byte("cifrado"), TokenHint: "CDEF", ConnectedBy: actor, ConnectedAt: time.Now(),
	}
	c.SetZones([]domain.DNSZone{{ID: "a", Name: "acme.test"}}, time.Now())
	count := func(subject string) int {
		var n int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM platform.event_outbox WHERE tenant_id = $1 AND subject = $2`, tenantID, subject).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	boom := errors.New("fallo despues de encolar")
	err := repo.Transact(ctx, func(ctx context.Context) error {
		if err := repo.SaveDNSProvider(ctx, c); err != nil {
			return err
		}
		if err := events.DNSProviderConnected(ctx, c); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) || count(outboxadapter.SubjectDNSProviderConnected) != 0 {
		t.Fatalf("revertida: %v %d", err, count(outboxadapter.SubjectDNSProviderConnected))
	}
	if _, err := repo.GetDNSProvider(ctx, tenantID, domain.DNSProviderCloudflare); !errors.Is(err, domain.ErrDNSProviderNotConnected) {
		t.Error("la conexion quedo sin su evento")
	}
	err = repo.Transact(ctx, func(ctx context.Context) error {
		if err := repo.SaveDNSProvider(ctx, c); err != nil {
			return err
		}
		return events.DNSProviderConnected(ctx, c)
	})
	if err != nil || count(outboxadapter.SubjectDNSProviderConnected) != 1 {
		t.Fatalf("confirmada: %v", err)
	}
	var payload string
	if err := q.QueryRow(ctx, `SELECT payload::text FROM platform.event_outbox WHERE tenant_id = $1 AND subject = $2`,
		tenantID, outboxadapter.SubjectDNSProviderConnected).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "cifrado") || strings.Contains(payload, "CDEF") || !strings.Contains(payload, actor.String()) ||
		!strings.Contains(payload, `"zones_visible": 1`) {
		t.Errorf("payload %s", payload)
	}
}
