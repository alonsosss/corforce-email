//go:build integration

package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
)

func rotationRing(t *testing.T, active, old string) *crypto.KeyRing {
	t.Helper()
	t.Setenv("DOMAIN_ROTATION_KEY", active)
	t.Setenv("DOMAIN_ROTATION_KEYS_OLD", old)
	kr, err := crypto.LoadKeyRing("DOMAIN_ROTATION_KEY", "DOMAIN_ROTATION_KEYS_OLD")
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

// Las columnas que declara SealedColumns existen en el esquema real y, re-cifradas, las lee el
// repositorio con la llave nueva sola: la clave DKIM vigente, la anterior en gracia y el token del
// proveedor DNS. Una segunda pasada no re-cifra nada de lo suyo.
func TestLasColumnasCifradasSeRecifranBajoLaLlaveActiva(t *testing.T) {
	ctx, repo := setup(t)
	pool, _ := db.PoolFromCtx(ctx)
	oldKey, newKey := strings.Repeat("a1", 32), strings.Repeat("b2", 32)
	old := rotationRing(t, oldKey, "")
	seal := func(s string) []byte {
		enc, err := old.Encrypt([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return enc
	}

	tenantID := uuid.New()
	d := sample(tenantID, "rota-"+uuid.NewString()[:8]+".test")
	d.DKIMPrivateKeyEnc = seal("CLAVE-VIGENTE")
	if err := repo.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE domains.domains SET dkim_previous_selector = 'cfm202601', dkim_previous_private_key_enc = $2,
		        dkim_previous_public_key = 'PUB0', dkim_rotated_at = now() WHERE id = $1`, d.ID, seal("CLAVE-ANTERIOR")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	conn := &domain.DNSProviderConnection{
		ID: uuid.New(), TenantID: tenantID, Provider: domain.DNSProviderCloudflare, TokenEnc: seal("TOKEN-DNS"),
		TokenHint: "DNS1", ConnectedBy: uuid.New(), ConnectedAt: now,
	}
	conn.SetZones([]domain.DNSZone{{ID: "a", Name: "acme.test"}}, now)
	if err := repo.SaveDNSProvider(ctx, conn); err != nil {
		t.Fatal(err)
	}

	rotating := rotationRing(t, newKey, oldKey)
	rotated := 0
	for _, target := range SealedColumns() {
		if target.AAD != nil {
			t.Fatal("domain-service cifra sin datos autenticados")
		}
		rep, err := crypto.RotateStore(ctx, rotating, target.Column, uuid.Nil, 50, func(uuid.UUID) []byte { return nil })
		if err != nil {
			t.Fatal(err)
		}
		rotated += rep.Rotated
	}
	if rotated < 3 {
		t.Fatalf("re-cifrados %d, se esperaban al menos 3", rotated)
	}

	retired := rotationRing(t, newKey, "")
	got, err := repo.GetByID(ctx, tenantID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	gotConn, err := repo.GetDNSProvider(ctx, tenantID, domain.DNSProviderCloudflare)
	if err != nil {
		t.Fatal(err)
	}
	for want, enc := range map[string][]byte{
		"CLAVE-VIGENTE": got.DKIMPrivateKeyEnc, "CLAVE-ANTERIOR": got.DKIMPreviousPrivateKeyEnc, "TOKEN-DNS": gotConn.TokenEnc,
	} {
		if plain, err := retired.Decrypt(enc); err != nil || string(plain) != want {
			t.Fatalf("%s sin la llave vieja: %q %v", want, plain, err)
		}
	}
	for _, target := range SealedColumns() {
		rep, err := crypto.RotateStore(ctx, rotating, target.Column, uuid.Nil, 50, func(uuid.UUID) []byte { return nil })
		if err != nil || rep.Rotated != 0 {
			t.Fatalf("segunda pasada: %+v %v", rep, err)
		}
	}
}
