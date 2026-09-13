//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/ports"
	"github.com/alonsosss/corforce-email/services/contacts/internal/segment"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CONTACTS_TEST_DSN apunta a una base con migrations/tenant/canonical/platform/00_outbox.sql
// y migrations/tenant/canonical/contacts/01_contacts.sql aplicadas.
func testPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	dsn := os.Getenv("CONTACTS_TEST_DSN")
	if dsn == "" {
		t.Skip("CONTACTS_TEST_DSN no definido")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, db.WithPool(ctx, pool)
}

func strp(s string) *string { return &s }

func newContact(tenant uuid.UUID, email string) *domain.Contact {
	return &domain.Contact{TenantID: tenant, Email: email, Status: domain.StatusActive, Source: domain.SourceAPI, Attributes: map[string]any{}, Tags: []string{}}
}

func insert(t *testing.T, ctx context.Context, repo *ContactRepository, c *domain.Contact) *domain.Contact {
	t.Helper()
	if err := repo.Insert(ctx, c); err != nil {
		t.Fatalf("insert %s: %v", c.Email, err)
	}
	return c
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func TestContactosCRUD(t *testing.T) {
	_, ctx := testPool(t)
	cp := &db.ContextPool{}
	repo := NewContactRepository(cp)
	lists := NewListRepository(cp)
	tenant := uuid.New()

	c := newContact(tenant, "ana@example.com")
	c.FirstName, c.Locale, c.Tags = "Ana", strp("es-PE"), []string{"vip", "feria"}
	c.Attributes = map[string]any{"plan": "pro", "score": json.Number("12345678901234567890.125")}
	insert(t, ctx, repo, c)
	if c.ID == uuid.Nil || c.ConsentStatus != domain.ConsentNone || c.CreatedAt.IsZero() {
		t.Fatalf("insert: %+v", c)
	}
	if err := repo.Insert(ctx, newContact(tenant, "ana@example.com")); !errors.Is(err, domain.ErrContactExists) {
		t.Fatalf("duplicado: %v", err)
	}
	if err := repo.Insert(ctx, newContact(tenant, "MAYUS@example.com")); err == nil {
		t.Fatal("el CHECK de minusculas debe rechazar la fila")
	}
	bad := newContact(tenant, "estado@example.com")
	bad.Status = "deleted"
	if err := repo.Insert(ctx, bad); err == nil {
		t.Fatal("el CHECK de status debe rechazar la fila")
	}

	got, err := repo.GetByID(ctx, tenant, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Attributes["score"] != json.Number("12345678901234567890.125") || got.Attributes["plan"] != "pro" {
		t.Fatalf("los numeros no pasan por float64: %#v", got.Attributes)
	}
	if strings.Join(got.Tags, ",") != "vip,feria" || *got.Locale != "es-PE" || got.Timezone != nil {
		t.Fatalf("lectura: %+v", got)
	}
	if _, err := repo.GetByID(ctx, uuid.New(), c.ID); !errors.Is(err, domain.ErrContactNotFound) {
		t.Fatalf("otra empresa no ve la fila: %v", err)
	}

	got.LastName, got.Timezone, got.Status = "Perez", strp("America/Lima"), domain.StatusBounced
	if err := repo.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, _ := repo.GetByID(ctx, tenant, c.ID)
	if again.LastName != "Perez" || *again.Timezone != "America/Lima" || again.Status != domain.StatusBounced || !again.UpdatedAt.After(again.CreatedAt) {
		t.Fatalf("update: %+v", again)
	}

	pct := newContact(tenant, "50pct@example.com")
	pct.FirstName = "50%_off"
	insert(t, ctx, repo, pct)
	l := &domain.List{TenantID: tenant, Name: "Clientes"}
	if err := lists.Create(ctx, l); err != nil {
		t.Fatal(err)
	}
	if n, err := lists.AddMembers(ctx, tenant, l.ID, []uuid.UUID{pct.ID, uuid.New()}); err != nil || n != 1 {
		t.Fatalf("AddMembers: %d %v", n, err)
	}

	for _, tc := range []struct {
		f    ports.ContactFilter
		want int64
	}{
		{ports.ContactFilter{}, 2},
		{ports.ContactFilter{Status: domain.StatusBounced}, 1},
		{ports.ContactFilter{Tag: "vip"}, 1},
		{ports.ContactFilter{Search: "%"}, 1},
		{ports.ContactFilter{Search: "_o"}, 1},
		{ports.ContactFilter{Search: "perez"}, 1},
		{ports.ContactFilter{ListID: &l.ID}, 1},
	} {
		tc.f.Page, tc.f.PerPage = 1, 10
		_, total, err := repo.List(ctx, tenant, tc.f)
		if err != nil || total != tc.want {
			t.Errorf("List %+v: total=%d err=%v", tc.f, total, err)
		}
	}

	// Lotes: una direccion ya existente se omite; arrays y nulos viajan por el JSON.
	batch := []domain.Contact{*newContact(tenant, "ana@example.com"), *newContact(tenant, "b1@example.com"), *newContact(tenant, "b2@example.com")}
	batch[1].Tags, batch[1].Attributes, batch[1].Locale = []string{"a", "b"}, map[string]any{"score": json.Number("3")}, strp("pt-BR")
	batch[1].Source, batch[2].Source = domain.SourceImport, domain.SourceImport
	inserted, err := repo.InsertMany(ctx, batch)
	if err != nil || len(inserted) != 2 {
		t.Fatalf("InsertMany: %d %v", len(inserted), err)
	}
	var b1 domain.Contact
	for _, x := range inserted {
		if x.Email == "b1@example.com" {
			b1 = x
		}
	}
	if strings.Join(b1.Tags, ",") != "a,b" || b1.Attributes["score"] != json.Number("3") || *b1.Locale != "pt-BR" || b1.Source != domain.SourceImport {
		t.Fatalf("fila del lote: %+v", b1)
	}
	b1.FirstName, b1.Tags, b1.Locale = "Bea", []string{"c"}, nil
	if err := repo.UpdateMany(ctx, []domain.Contact{b1}); err != nil {
		t.Fatal(err)
	}
	if x, _ := repo.GetByID(ctx, tenant, b1.ID); x.FirstName != "Bea" || strings.Join(x.Tags, ",") != "c" || x.Locale != nil {
		t.Fatalf("UpdateMany: %+v", x)
	}

	err = cp.Transact(ctx, func(ctx context.Context) error {
		found, err := repo.FindByEmailsForUpdate(ctx, tenant, []string{"ana@example.com", "b2@example.com", "nadie@example.com"})
		if err != nil {
			return err
		}
		if len(found) != 2 {
			return fmt.Errorf("FindByEmailsForUpdate: %d", len(found))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := repo.StripAttribute(ctx, tenant, "score"); err != nil {
		t.Fatal(err)
	}
	if x, _ := repo.GetByID(ctx, tenant, c.ID); x.Attributes["score"] != nil || x.Attributes["plan"] != "pro" {
		t.Fatalf("StripAttribute: %#v", x.Attributes)
	}
}

func TestConsentimientoEsEvidenciaAppendOnly(t *testing.T) {
	pool, ctx := testPool(t)
	cp := &db.ContextPool{}
	contacts := NewContactRepository(cp)
	consents := NewConsentRepository(cp)
	tenant := uuid.New()
	c := insert(t, ctx, contacts, newContact(tenant, "evidencia@example.com"))

	granted := &domain.Consent{TenantID: tenant, ContactID: c.ID, Purpose: domain.PurposeMarketing, Status: domain.ConsentGranted,
		Method: domain.MethodForm, Source: "https://example.com/alta", IP: strp("203.0.113.4"), UserAgent: strp("Mozilla/5.0"),
		Evidence: map[string]any{"checkbox": true}}
	if err := consents.Append(ctx, granted); err != nil {
		t.Fatal(err)
	}
	if x, _ := contacts.GetByID(ctx, tenant, c.ID); x.ConsentStatus != domain.ConsentGranted {
		t.Fatalf("el trigger proyecta el vigente: %s", x.ConsentStatus)
	}
	if err := consents.AppendMany(ctx, []domain.Consent{{TenantID: tenant, ContactID: c.ID, Purpose: domain.PurposeMarketing,
		Status: domain.ConsentRevoked, Method: domain.MethodSuppression, Source: "suppression:ses"}}); err != nil {
		t.Fatal(err)
	}
	if x, _ := contacts.GetByID(ctx, tenant, c.ID); x.ConsentStatus != domain.ConsentRevoked {
		t.Fatalf("la ultima fila es la vigente: %s", x.ConsentStatus)
	}
	rows, err := consents.ListByContact(ctx, tenant, c.ID)
	if err != nil || len(rows) != 2 || *rows[0].IP != "203.0.113.4" || rows[0].Evidence["checkbox"] != true || rows[1].IP != nil {
		t.Fatalf("historial: %+v %v", rows, err)
	}

	// Sin app.erasure, la tabla no admite modificar ni borrar, ni siquiera con la
	// credencial de la aplicacion.
	for _, sql := range []string{
		`UPDATE contacts.consents SET status = 'granted' WHERE contact_id = $1`,
		`DELETE FROM contacts.consents WHERE contact_id = $1`,
	} {
		if _, err := pool.Exec(ctx, sql, c.ID); sqlState(err) != "42501" {
			t.Fatalf("%s: se esperaba insufficient_privilege, hubo %v", sql, err)
		}
	}
	if _, err := pool.Exec(ctx, `TRUNCATE contacts.consents`); sqlState(err) != "42501" {
		t.Fatalf("TRUNCATE: %v", err)
	}
	// Con app.erasure solo se seudonimiza: cambiar la decision sigue prohibido.
	err = cp.Transact(ctx, func(ctx context.Context) error {
		if _, err := cp.Exec(ctx, `SET LOCAL app.erasure = 'on'`); err != nil {
			return err
		}
		_, err := cp.Exec(ctx, `UPDATE contacts.consents SET status = 'granted' WHERE contact_id = $1`, c.ID)
		return err
	})
	if sqlState(err) != "42501" {
		t.Fatalf("el borrado no puede cambiar la decision: %v", err)
	}
	if rows, _ := consents.ListByContact(ctx, tenant, c.ID); len(rows) != 2 || rows[1].Status != domain.ConsentRevoked {
		t.Fatalf("la evidencia sigue intacta: %+v", rows)
	}
}

func TestBorradoDelTitularSeudonimiza(t *testing.T) {
	pool, ctx := testPool(t)
	cp := &db.ContextPool{}
	contacts := NewContactRepository(cp)
	consents := NewConsentRepository(cp)
	tokens := NewTokenRepository(cp)
	lists := NewListRepository(cp)
	tenant := uuid.New()

	c := insert(t, ctx, contacts, newContact(tenant, "borrar@example.com"))
	for _, st := range []domain.ConsentStatus{domain.ConsentPending, domain.ConsentGranted} {
		if err := consents.Append(ctx, &domain.Consent{TenantID: tenant, ContactID: c.ID, Purpose: domain.PurposeMarketing, Status: st,
			Method: domain.MethodDoubleOptIn, Source: "x", IP: strp("198.51.100.7"), UserAgent: strp("UA"), Evidence: map[string]any{"k": "v"}}); err != nil {
			t.Fatal(err)
		}
	}
	l := &domain.List{TenantID: tenant, Name: "L"}
	if err := lists.Create(ctx, l); err != nil {
		t.Fatal(err)
	}
	if _, err := lists.AddMembers(ctx, tenant, l.ID, []uuid.UUID{c.ID}); err != nil {
		t.Fatal(err)
	}
	if err := tokens.Create(ctx, &domain.ConfirmationToken{TenantID: tenant, ContactID: c.ID, TokenHash: strings.Repeat("ab", 32), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	if err := contacts.Erase(ctx, tenant, c.ID, uuid.New(), "x"); err == nil {
		t.Fatal("fuera de una transaccion el borrado debe negarse")
	}
	pseudonym, sha := uuid.New(), domain.EmailSHA256(c.Email)
	if err := cp.Transact(ctx, func(ctx context.Context) error { return contacts.Erase(ctx, tenant, c.ID, pseudonym, sha) }); err != nil {
		t.Fatal(err)
	}

	if _, err := contacts.GetByID(ctx, tenant, c.ID); !errors.Is(err, domain.ErrContactNotFound) {
		t.Fatalf("el contacto debe desaparecer: %v", err)
	}
	var members, toks, linked int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM contacts.list_members WHERE contact_id = $1),
		(SELECT count(*) FROM contacts.confirmation_tokens WHERE contact_id = $1),
		(SELECT count(*) FROM contacts.consents WHERE contact_id = $1)`, c.ID).Scan(&members, &toks, &linked); err != nil {
		t.Fatal(err)
	}
	if members != 0 || toks != 0 || linked != 0 {
		t.Fatalf("restos del titular: miembros=%d tokens=%d consents=%d", members, toks, linked)
	}
	kept, err := consents.ListByContact(ctx, tenant, pseudonym)
	if err != nil || len(kept) != 2 {
		t.Fatalf("la evidencia se conserva: %d %v", len(kept), err)
	}
	for _, k := range kept {
		if k.IP != nil || k.UserAgent != nil || k.Evidence["email_sha256"] != sha || k.Evidence["k"] != "v" || k.Evidence["erased_at"] == nil {
			t.Fatalf("seudonimizacion: %+v", k)
		}
	}
	if kept[0].Status != domain.ConsentPending || kept[1].Status != domain.ConsentGranted {
		t.Fatalf("la decision y su orden se conservan: %+v", kept)
	}
	// El permiso del borrado no sobrevive a su transaccion.
	if _, err := pool.Exec(ctx, `UPDATE contacts.consents SET ip = NULL WHERE contact_id = $1`, pseudonym); sqlState(err) != "42501" {
		t.Fatalf("app.erasure no debe filtrarse fuera de la transaccion: %v", err)
	}
}

func TestTokensDeConfirmacion(t *testing.T) {
	_, ctx := testPool(t)
	cp := &db.ContextPool{}
	contacts := NewContactRepository(cp)
	tokens := NewTokenRepository(cp)
	tenant := uuid.New()
	c := insert(t, ctx, contacts, newContact(tenant, "token@example.com"))
	hash := strings.Repeat("0f", 32)
	tk := &domain.ConfirmationToken{TenantID: tenant, ContactID: c.ID, TokenHash: hash, ExpiresAt: time.Now().Add(time.Hour)}
	if err := tokens.Create(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if err := tokens.Create(ctx, &domain.ConfirmationToken{TenantID: tenant, ContactID: c.ID, TokenHash: "NO-HEX", ExpiresAt: time.Now()}); err == nil {
		t.Fatal("el CHECK del hash debe rechazar un valor que no es sha256 hex")
	}
	if _, err := tokens.GetByHash(ctx, uuid.New(), hash); !errors.Is(err, domain.ErrInvalidConfirmation) {
		t.Fatalf("otra empresa: %v", err)
	}
	got, err := tokens.GetByHash(ctx, tenant, hash)
	if err != nil || got.ContactID != c.ID || got.UsedAt != nil {
		t.Fatalf("GetByHash: %+v %v", got, err)
	}
	if err := tokens.MarkUsed(ctx, tenant, tk.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := tokens.MarkUsed(ctx, tenant, tk.ID, time.Now()); !errors.Is(err, domain.ErrInvalidConfirmation) {
		t.Fatalf("un token solo se usa una vez: %v", err)
	}
	if err := tokens.DeleteUnused(ctx, tenant, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tokens.GetByHash(ctx, tenant, hash); err != nil {
		t.Fatalf("un token usado se conserva: %v", err)
	}
}

// seedSegmentData crea cuatro contactos con atributos, etiquetas y una lista.
func seedSegmentData(t *testing.T, ctx context.Context, cp *db.ContextPool, tenant uuid.UUID) (segment.Schema, uuid.UUID) {
	t.Helper()
	contacts := NewContactRepository(cp)
	attrs := NewAttributeRepository(cp)
	lists := NewListRepository(cp)
	for key, typ := range map[string]domain.AttrType{"plan": domain.AttrString, "score": domain.AttrNumber, "vip": domain.AttrBoolean, "since": domain.AttrDate} {
		if err := attrs.Create(ctx, &domain.AttributeDefinition{TenantID: tenant, Key: key, Type: typ}); err != nil {
			t.Fatal(err)
		}
	}
	a := newContact(tenant, "a@example.com")
	a.Locale, a.Tags = strp("es-PE"), []string{"vip", "feria"}
	a.Attributes = map[string]any{"plan": "pro", "score": json.Number("10"), "vip": true, "since": "2024-01-10"}
	b := newContact(tenant, "b@example.com")
	b.Tags, b.Attributes = []string{"feria"}, map[string]any{"plan": "pro", "score": json.Number("3")}
	c := newContact(tenant, "c@example.com")
	c.FirstName, c.Attributes = "50%_off", map[string]any{"plan": "free", "score": json.Number("50"), "vip": false}
	d := newContact(tenant, "d@example.com")
	d.Tags, d.Status = []string{"vip"}, domain.StatusUnsubscribed
	for _, x := range []*domain.Contact{a, b, c, d} {
		insert(t, ctx, contacts, x)
	}
	l := &domain.List{TenantID: tenant, Name: "Clientes"}
	if err := lists.Create(ctx, l); err != nil {
		t.Fatal(err)
	}
	if _, err := lists.AddMembers(ctx, tenant, l.ID, []uuid.UUID{a.ID, c.ID}); err != nil {
		t.Fatal(err)
	}
	return segment.Schema{
		Attributes: map[string]segment.AttrType{"plan": segment.AttrString, "score": segment.AttrNumber, "vip": segment.AttrBoolean, "since": segment.AttrDate},
		Enums: map[string][]string{
			"status":  {"active", "unsubscribed", "bounced", "complained"},
			"source":  {"api", "import", "form", "integration"},
			"consent": {"granted", "revoked", "pending", "none"},
		},
	}, l.ID
}

func TestSegmentoRealSobreAtributosYTags(t *testing.T) {
	_, ctx := testPool(t)
	cp := &db.ContextPool{}
	tenant := uuid.New()
	schema, listID := seedSegmentData(t, ctx, cp, tenant)
	// Otra empresa con los mismos datos: no debe aparecer en ningun resultado.
	seedSegmentData(t, ctx, cp, uuid.New())
	q := NewSegmentQuery(cp)

	cases := []struct {
		def  string
		want string
	}{
		{`{"match":"all","rules":[{"field":"attributes.plan","op":"eq","value":"pro"}]}`, "a,b"},
		{`{"match":"all","rules":[{"field":"attributes.plan","op":"eq","value":"pro"},{"field":"attributes.score","op":"gt","value":5}]}`, "a"},
		{`{"match":"any","rules":[{"field":"tags","op":"has_tag","value":"VIP"},{"field":"attributes.score","op":"gte","value":50}]}`, "a,c,d"},
		{`{"match":"all","rules":[{"field":"tags","op":"neq","value":"feria"}]}`, "c,d"},
		{`{"match":"all","rules":[{"field":"tags","op":"in","value":["feria","otra"]}]}`, "a,b"},
		{`{"match":"all","rules":[{"field":"tags","op":"not_exists"}]}`, "c"},
		{`{"match":"all","rules":[{"field":"locale","op":"not_exists"}]}`, "b,c,d"},
		{`{"match":"all","rules":[{"field":"locale","op":"starts_with","value":"es"}]}`, "a"},
		{`{"match":"all","rules":[{"field":"locale","op":"neq","value":"es-pe"}]}`, "b,c,d"},
		{`{"match":"all","rules":[{"field":"first_name","op":"contains","value":"%_"}]}`, "c"},
		{`{"match":"all","rules":[{"field":"first_name","op":"exists"}]}`, "c"},
		{`{"match":"all","rules":[{"field":"email","op":"starts_with","value":"_"}]}`, ""},
		{`{"match":"all","rules":[{"field":"email","op":"in","value":["A@example.com","zz@example.com"]}]}`, "a"},
		{fmt.Sprintf(`{"match":"all","rules":[{"field":"list","op":"in_list","value":"%s"}]}`, listID), "a,c"},
		{fmt.Sprintf(`{"match":"all","rules":[{"field":"list","op":"not_in_list","value":"%s"}]}`, listID), "b,d"},
		{`{"match":"all","rules":[{"field":"attributes.since","op":"lt","value":"2025-01-01"}]}`, "a"},
		{`{"match":"all","rules":[{"field":"attributes.since","op":"in","value":["2024-01-10"]}]}`, "a"},
		{`{"match":"all","rules":[{"field":"attributes.vip","op":"eq","value":true}]}`, "a"},
		{`{"match":"all","rules":[{"field":"attributes.vip","op":"neq","value":true}]}`, "b,c,d"},
		{`{"match":"all","rules":[{"field":"attributes.score","op":"in","value":[3,50]}]}`, "b,c"},
		{`{"match":"all","rules":[{"field":"attributes.score","op":"eq","value":10.0}]}`, "a"},
		{`{"match":"all","rules":[{"field":"attributes.plan","op":"contains","value":"RO"}]}`, "a,b"},
		{`{"match":"all","rules":[{"field":"attributes.plan","op":"in","value":["free"]}]}`, "c"},
		{`{"match":"all","rules":[{"field":"attributes.vip","op":"exists"}]}`, "a,c"},
		{`{"match":"all","rules":[{"field":"attributes.plan","op":"neq","value":"pro"}]}`, "c,d"},
		{`{"match":"all","rules":[{"field":"status","op":"eq","value":"active"},{"field":"consent","op":"eq","value":"none"}]}`, "a,b,c"},
		{`{"match":"all","rules":[{"field":"created_at","op":"gte","value":"2000-01-01"}]}`, "a,b,c,d"},
		{`{"match":"all","rules":[{"field":"attributes.plan","op":"eq","value":"pro"},{"match":"any","rules":[{"field":"tags","op":"has_tag","value":"vip"},{"field":"locale","op":"not_exists"}]}]}`, "a,b"},
	}
	for _, tc := range cases {
		def, err := segment.Parse([]byte(tc.def))
		if err != nil {
			t.Fatalf("%s: %v", tc.def, err)
		}
		n, err := q.Count(ctx, tenant, def, schema)
		if err != nil {
			t.Fatalf("%s: %v", tc.def, err)
		}
		rows, err := q.Page(ctx, tenant, def, schema, 100, 0)
		if err != nil {
			t.Fatalf("%s: %v", tc.def, err)
		}
		var got []string
		for _, r := range rows {
			got = append(got, strings.TrimSuffix(r.Email, "@example.com"))
		}
		sort.Strings(got)
		if strings.Join(got, ",") != tc.want || int(n) != len(got) {
			t.Errorf("%s\n got: %v (count %d)\nwant: %s", tc.def, got, n, tc.want)
		}
	}
}

func TestAudienciaSobreVeinteMilContactos(t *testing.T) {
	pool, ctx := testPool(t)
	cp := &db.ContextPool{}
	q := NewSegmentQuery(cp)
	tenant, other := uuid.New(), uuid.New()

	// Prueba controlada: 20.000 contactos sinteticos de la empresa y 5.000 de otra.
	// status: 1 de cada 10 dado de baja; consentimiento concedido en 4 de cada 5
	// (por la tabla de evidencia, para que el trigger proyecte el vigente); la mitad
	// en la lista; score = g % 100; un tercio con la etiqueta vip.
	seed := func(tid uuid.UUID, n int) {
		if _, err := pool.Exec(ctx, `
			INSERT INTO contacts.contacts (tenant_id, email, first_name, attributes, tags, status, source)
			SELECT $1, 'c' || g || '@example.com', 'N' || g, jsonb_build_object('score', g % 100),
			       CASE WHEN g % 3 = 0 THEN ARRAY['vip'] ELSE ARRAY[]::text[] END,
			       CASE WHEN g % 10 = 0 THEN 'unsubscribed' ELSE 'active' END, 'import'
			  FROM generate_series(1, $2::int) AS g`, tid, n); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO contacts.consents (tenant_id, contact_id, purpose, status, method, source)
			SELECT tenant_id, id, 'marketing', 'granted', 'import', 'prueba'
			  FROM contacts.contacts WHERE tenant_id = $1 AND substr(first_name, 2)::int % 5 <> 0`, tid); err != nil {
			t.Fatal(err)
		}
	}
	seed(tenant, 20000)
	seed(other, 5000)

	lists := NewListRepository(cp)
	l := &domain.List{TenantID: tenant, Name: "Pares"}
	if err := lists.Create(ctx, l); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO contacts.list_members (tenant_id, list_id, contact_id)
		SELECT tenant_id, $2, id FROM contacts.contacts WHERE tenant_id = $1 AND substr(first_name, 2)::int % 2 = 0`, tenant, l.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE contacts.contacts; ANALYZE contacts.list_members; ANALYZE contacts.consents`); err != nil {
		t.Fatal(err)
	}

	schema := segment.Schema{Attributes: map[string]segment.AttrType{"score": segment.AttrNumber}}
	include, _ := segment.Parse([]byte(`{"match":"all","rules":[{"field":"attributes.score","op":"gte","value":90}]}`))
	exclude, _ := segment.Parse([]byte(`{"match":"all","rules":[{"field":"tags","op":"has_tag","value":"vip"}]}`))
	spec := ports.AudienceSpec{ListIDs: []uuid.UUID{l.ID}, Include: []segment.Definition{include}, Exclude: []segment.Definition{exclude}, Schema: schema, Limit: 1000}

	var want int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM contacts.contacts
		 WHERE tenant_id = $1 AND status = 'active' AND marketing_consent = 'granted'
		   AND (substr(first_name, 2)::int % 2 = 0 OR (attributes->>'score')::int >= 90)
		   AND NOT ('vip' = ANY(tags))`, tenant).Scan(&want); err != nil {
		t.Fatal(err)
	}

	seen := map[uuid.UUID]bool{}
	var prev uuid.UUID
	pages := 0
	start := time.Now()
	for {
		rows, err := q.Audience(ctx, tenant, spec)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, c := range rows {
			if seen[c.ID] || bytes.Compare(c.ID[:], prev[:]) <= 0 {
				t.Fatalf("pagina %d: id repetido o fuera de orden", pages)
			}
			if c.TenantID != tenant || !c.Sendable() || containsTag(c.Tags, "vip") {
				t.Fatalf("contacto que no debia salir: %+v", c)
			}
			seen[c.ID], prev = true, c.ID
		}
		if len(rows) < spec.Limit {
			break
		}
		spec.After = prev
	}
	t.Logf("audiencia: %d contactos en %d paginas, %s en total", len(seen), pages, time.Since(start))
	if len(seen) != want || want == 0 {
		t.Fatalf("audiencia: %d contactos, se esperaban %d", len(seen), want)
	}

	// El plan de la primera pagina y de una intermedia: el filtro principal recorre el
	// indice parcial de enviables, sin leer la tabla entera.
	for _, after := range []uuid.UUID{uuid.Nil, prev} {
		spec.After = after
		sql, args, err := audienceSQL(tenant, spec)
		if err != nil {
			t.Fatal(err)
		}
		plan := explain(t, ctx, pool, sql, args)
		t.Logf("EXPLAIN (after=%s):\n%s", after, plan)
		if strings.Contains(plan, "Seq Scan on contacts c") {
			t.Fatalf("seq scan en el filtro principal:\n%s", plan)
		}
		if !strings.Contains(plan, "idx_contacts_contacts_sendable") {
			t.Fatalf("el recorrido debe usar el indice de enviables:\n%s", plan)
		}
	}
}

func explain(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args []any) string {
	t.Helper()
	rows, err := pool.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING OFF) "+sql, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

func containsTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

func TestCatalogo(t *testing.T) {
	_, ctx := testPool(t)
	cp := &db.ContextPool{}
	lists := NewListRepository(cp)
	attrs := NewAttributeRepository(cp)
	segs := NewSegmentRepository(cp)
	imports := NewImportRepository(cp)
	tenant := uuid.New()

	l := &domain.List{TenantID: tenant, Name: "A"}
	if err := lists.Create(ctx, l); err != nil {
		t.Fatal(err)
	}
	if err := lists.Create(ctx, &domain.List{TenantID: tenant, Name: "A"}); !errors.Is(err, domain.ErrListExists) {
		t.Fatalf("lista duplicada: %v", err)
	}
	l.Name = "B"
	if err := lists.Update(ctx, l); err != nil {
		t.Fatal(err)
	}
	if ids, _ := lists.ExistingIDs(ctx, tenant, []uuid.UUID{l.ID, uuid.New()}); len(ids) != 1 {
		t.Fatalf("ExistingIDs: %v", ids)
	}
	if err := attrs.Create(ctx, &domain.AttributeDefinition{TenantID: tenant, Key: "Mal", Type: domain.AttrString}); err == nil {
		t.Fatal("el CHECK de la clave debe rechazar mayusculas")
	}
	if err := attrs.Create(ctx, &domain.AttributeDefinition{TenantID: tenant, Key: "plan", Type: domain.AttrString}); err != nil {
		t.Fatal(err)
	}
	if err := attrs.Create(ctx, &domain.AttributeDefinition{TenantID: tenant, Key: "plan", Type: domain.AttrNumber}); !errors.Is(err, domain.ErrAttributeExists) {
		t.Fatalf("atributo duplicado: %v", err)
	}
	s := &domain.Segment{TenantID: tenant, Name: "S", Definition: json.RawMessage(`{"match":"all","rules":[{"field":"tags","op":"exists"}]}`)}
	if err := segs.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	if got, err := segs.GetMany(ctx, tenant, []uuid.UUID{s.ID, uuid.New()}); err != nil || len(got) != 1 {
		t.Fatalf("GetMany: %d %v", len(got), err)
	}
	if err := segs.Delete(ctx, tenant, s.ID); err != nil {
		t.Fatal(err)
	}
	if err := lists.Delete(ctx, tenant, l.ID); err != nil {
		t.Fatal(err)
	}

	imp := &domain.Import{ID: uuid.New(), TenantID: tenant, Status: domain.ImportCompleted, Total: 3, Created: 1, Updated: 1, Skipped: 1,
		Errors: []domain.ImportError{{Line: 2, Reason: "direccion de correo no valida"}}, CreatedBy: uuid.New()}
	if err := imports.Create(ctx, imp); err != nil {
		t.Fatal(err)
	}
	if err := imports.Create(ctx, &domain.Import{ID: uuid.New(), TenantID: tenant, Status: domain.ImportCompleted, Total: 1, Created: 2, CreatedBy: uuid.New()}); err == nil {
		t.Fatal("el CHECK de conteos debe rechazar created+updated+skipped > total")
	}
	got, err := imports.Get(ctx, tenant, imp.ID)
	if err != nil || len(got.Errors) != 1 || got.Errors[0].Line != 2 || got.ListID != nil {
		t.Fatalf("importacion: %+v %v", got, err)
	}
}
