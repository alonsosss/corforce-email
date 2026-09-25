//go:build integration

package postgres

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/services/identity/internal/adapters/passwordhash"
	"github.com/alonsosss/corforce-email/services/identity/internal/app"
	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

const (
	mfaKeyOld = "1111111111111111111111111111111111111111111111111111111111111111"
	mfaKeyNew = "2222222222222222222222222222222222222222222222222222222222222222"
)

func ringOf(t *testing.T, active, old string) *crypto.KeyRing {
	t.Helper()
	t.Setenv("IDENTITY_IT_KEY", active)
	t.Setenv("IDENTITY_IT_KEYS_OLD", old)
	kr, err := crypto.LoadKeyRing("IDENTITY_IT_KEY", "IDENTITY_IT_KEYS_OLD")
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

// testSealer cifra con una llave de la prueba, como MAIL_ENCRYPTION_KEY en produccion.
func testSealer(t *testing.T) *crypto.KeyRing {
	return ringOf(t, strings.Repeat("3c", 32), "")
}

type mfaRow struct {
	enabled  bool
	plain    *string
	sealed   []byte
	lastStep int64
}

func readMFA(ctx context.Context, t *testing.T, pool *pgxpool.Pool, id uuid.UUID) mfaRow {
	t.Helper()
	var r mfaRow
	if err := pool.QueryRow(ctx,
		`SELECT mfa_enabled, mfa_secret, mfa_secret_enc, mfa_last_step FROM identity.users WHERE id = $1`, id,
	).Scan(&r.enabled, &r.plain, &r.sealed, &r.lastStep); err != nil {
		t.Fatal(err)
	}
	return r
}

func newMFAUseCase(t *testing.T, pool *pgxpool.Pool, sealer *crypto.KeyRing) *app.AuthUseCase {
	t.Helper()
	hasher, err := passwordhash.NewBcrypt(bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	uc, err := app.NewAuthUseCase(app.AuthDeps{
		Users: NewUserRepo(pool), Hasher: hasher, UnknownLogins: NewUnknownLoginRepo(pool), Sealer: sealer, Logger: zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return uc
}

// Las sentencias del segundo factor sobre el registro real: la activacion guarda solo el cifrado,
// el paso solo avanza, el perfil no toca el segundo factor y la baja borra el secreto.
func TestSentenciasDelSegundoFactorContraPostgres(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()
	tenant := uuid.New()
	cleanupTenant(t, pool, tenant)
	repo := NewUserRepo(pool)
	id := insertAccount(ctx, t, pool, tenant, "active", nil)

	if _, err := pool.Exec(ctx, `UPDATE identity.users SET mfa_secret = 'JBSWY3DPEHPK3PXP' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnableMFA(ctx, id, []byte("cifrado"), 100); err != nil {
		t.Fatal(err)
	}
	r := readMFA(ctx, t, pool, id)
	if !r.enabled || r.plain != nil || !bytes.Equal(r.sealed, []byte("cifrado")) || r.lastStep != 100 {
		t.Fatalf("activar: %+v", r)
	}
	for _, c := range []struct {
		step int64
		want bool
	}{{100, false}, {99, false}, {101, true}, {101, false}} {
		if ok, err := repo.AdvanceMFAStep(ctx, id, c.step); err != nil || ok != c.want {
			t.Fatalf("paso %d: %v %v, se esperaba %v", c.step, ok, err, c.want)
		}
	}

	u, err := repo.GetByID(ctx, id)
	if err != nil || !bytes.Equal(u.MFASecretSealed, []byte("cifrado")) || u.MFASecretLegacy != "" || u.MFALastStep != 101 {
		t.Fatalf("lectura: %+v %v", u, err)
	}
	// Una lectura anterior a la activacion no la deshace al guardar el perfil.
	u.MFAEnabled, u.MFASecretSealed, u.FirstName = false, nil, "Otro"
	if err := repo.Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	if r := readMFA(ctx, t, pool, id); !r.enabled || r.sealed == nil {
		t.Fatalf("el perfil toco el segundo factor: %+v", r)
	}

	if err := repo.DisableMFA(ctx, id); err != nil {
		t.Fatal(err)
	}
	if r := readMFA(ctx, t, pool, id); r.enabled || r.plain != nil || r.sealed != nil {
		t.Fatalf("desactivar: %+v", r)
	}
	if _, err := pool.Exec(ctx, `UPDATE identity.users SET mfa_last_step = -1 WHERE id = $1`, id); err == nil {
		t.Fatal("paso negativo aceptado")
	}
}

// El barrido cifra lo que queda en claro con los datos de su fila, borra el secreto de quien no
// tiene segundo factor activo y es idempotente; la rotacion re-cifra bajo la llave activa, no
// pisa lo que otro escribio y cuenta lo que no abre ninguna llave.
func TestBarridoYRotacionDelSecretoContraPostgres(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()
	tenant := uuid.New()
	cleanupTenant(t, pool, tenant)

	legado := insertAccount(ctx, t, pool, tenant, "active", nil)
	apagada := insertAccount(ctx, t, pool, tenant, "active", nil)
	ilegible := insertAccount(ctx, t, pool, tenant, "active", nil)
	for id, sql := range map[uuid.UUID]string{
		legado:   `UPDATE identity.users SET mfa_enabled = TRUE, mfa_secret = 'JBSWY3DPEHPK3PXP' WHERE id = $1`,
		apagada:  `UPDATE identity.users SET mfa_secret = 'GEZDGNBVGY3TQOJQ', mfa_secret_enc = '\x00' WHERE id = $1`,
		ilegible: `UPDATE identity.users SET mfa_enabled = TRUE, mfa_secret_enc = '\x0102' WHERE id = $1`,
	} {
		if _, err := pool.Exec(ctx, sql, id); err != nil {
			t.Fatal(err)
		}
	}

	oldRing := ringOf(t, mfaKeyOld, "")
	sweep, err := newMFAUseCase(t, pool, oldRing).SealLegacyMFASecrets(ctx, 1)
	if err != nil || sweep.Sealed < 1 || sweep.Dropped < 1 {
		t.Fatalf("barrido: %+v %v", sweep, err)
	}
	r := readMFA(ctx, t, pool, legado)
	if r.plain != nil || r.sealed == nil || bytes.Contains(r.sealed, []byte("JBSWY3DPEHPK3PXP")) {
		t.Fatalf("legado tras el barrido: %+v", r)
	}
	if plain, err := oldRing.DecryptWithAAD(r.sealed, domain.MFASecretAAD(legado)); err != nil || string(plain) != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("abrir el cifrado del barrido: %q %v", plain, err)
	}
	if r := readMFA(ctx, t, pool, apagada); r.plain != nil || r.sealed != nil {
		t.Fatalf("cuenta sin segundo factor con secreto: %+v", r)
	}
	if again, err := newMFAUseCase(t, pool, oldRing).SealLegacyMFASecrets(ctx, 1); err != nil || again.Sealed != 0 || again.Dropped != 0 {
		t.Fatalf("segunda pasada: %+v %v", again, err)
	}

	rotating := ringOf(t, mfaKeyNew, mfaKeyOld)
	store := NewMFASecretStore(pool)
	rep, err := crypto.RotateStore(ctx, rotating, store, uuid.Nil, 1, domain.MFASecretAAD)
	if err != nil || rep.Rotated < 1 || rep.Pending < 1 {
		t.Fatalf("rotacion: %+v %v", rep, err)
	}
	rotated := readMFA(ctx, t, pool, legado)
	if plain, err := ringOf(t, mfaKeyNew, "").DecryptWithAAD(rotated.sealed, domain.MFASecretAAD(legado)); err != nil || string(plain) != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("sin la llave vieja: %q %v", plain, err)
	}
	again, err := crypto.RotateStore(ctx, rotating, store, uuid.Nil, 1, domain.MFASecretAAD)
	if err != nil || again.Rotated != 0 || again.Pending != rep.Pending {
		t.Fatalf("segunda rotacion: %+v %v", again, err)
	}
	if ok, err := store.ReplaceSealed(ctx, legado, r.sealed, []byte("pisado")); err != nil || ok {
		t.Fatalf("sustituir un cifrado que ya cambio: %v %v", ok, err)
	}
	if got := readMFA(ctx, t, pool, legado); !bytes.Equal(got.sealed, rotated.sealed) {
		t.Fatal("la sustitucion condicional piso la fila")
	}
}

// Un texto vacio en la columna antigua (lo deja la version anterior al guardar el perfil de una
// cuenta ya cifrada) no es un secreto: el barrido no lo sella encima del cifrado y lo limpia.
func TestElBarridoNoSellaUnSecretoVacio(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()
	tenant := uuid.New()
	cleanupTenant(t, pool, tenant)
	repo := NewUserRepo(pool)
	id := insertAccount(ctx, t, pool, tenant, "active", nil)
	if err := repo.EnableMFA(ctx, id, []byte("cifrado"), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE identity.users SET mfa_secret = '' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	plains, err := repo.ListPlainMFASecrets(ctx, uuid.Nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plains {
		if p.UserID == id {
			t.Fatal("un secreto vacio no se lista para sellarlo")
		}
	}
	if _, err := repo.DropDisabledMFASecrets(ctx); err != nil {
		t.Fatal(err)
	}
	if r := readMFA(ctx, t, pool, id); !r.enabled || r.plain != nil || !bytes.Equal(r.sealed, []byte("cifrado")) {
		t.Fatalf("el cifrado se conserva y el texto vacio se limpia: %+v", r)
	}
}
