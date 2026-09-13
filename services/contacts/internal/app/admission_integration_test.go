//go:build integration

// Alta e importacion contra un Postgres real, con suppression sustituido por el doble de
// fakes_test.go:
//
//	CONTACTS_TEST_DSN=postgres://... go test -tags integration ./services/contacts/...
//
// La base necesita las migraciones de platform y contacts (make test-integration las aplica).
// Comprueba que el estado con que entra cada contacto, la evidencia, el rastro de la
// importacion y los eventos quedan en la base, y que un suppression caido no deja contactos
// sin comprobar.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	outboxadapter "github.com/alonsosss/corforce-email/services/contacts/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/contacts/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// integrationEnv devuelve la variable de entorno que apunta a la infraestructura de la
// prueba. Sin ella la prueba se salta, salvo con INTEGRATION_REQUIRED=1 (make
// test-integration y CI): ahi es un fallo, porque un salto esconderia que no llego.
func integrationEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		if os.Getenv("INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("%s no definida con INTEGRATION_REQUIRED=1", name)
		}
		t.Skipf("%s no definida", name)
	}
	return v
}

type dbAdmission struct {
	uc     *UseCase
	sup    *fakeSuppression
	pool   *pgxpool.Pool
	ctx    context.Context
	tenant uuid.UUID
}

func setupAdmission(t *testing.T) *dbAdmission {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), integrationEnv(t, "CONTACTS_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tenant := uuid.New()
	cp := &db.ContextPool{}
	sup := &fakeSuppression{causes: map[string][]domain.ActiveCause{}}
	uc := New(Deps{
		Contacts: postgres.NewContactRepository(cp), Consents: postgres.NewConsentRepository(cp),
		Tokens: postgres.NewTokenRepository(cp), Lists: postgres.NewListRepository(cp),
		Attributes: postgres.NewAttributeRepository(cp), Segments: postgres.NewSegmentRepository(cp),
		Query: postgres.NewSegmentQuery(cp), Imports: postgres.NewImportRepository(cp),
		Tx: cp, Events: outboxadapter.NewPublisher(cp), Suppression: sup,
		Config: Config{PublicBaseURL: "https://app.example.com", ImportMaxRows: 2 * maxCheckEmails},
	})
	ctx := middleware.WithIdentity(db.WithPool(context.Background(), pool), uuid.NewString(), tenant.String())
	return &dbAdmission{uc: uc, sup: sup, pool: pool, ctx: ctx, tenant: tenant}
}

func (d *dbAdmission) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := d.pool.QueryRow(d.ctx, sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// stored lee de la base el estado y el consentimiento proyectado de la direccion; "" si no
// existe.
func (d *dbAdmission) stored(t *testing.T, email string) (domain.Status, domain.ConsentStatus) {
	t.Helper()
	var (
		st domain.Status
		cs domain.ConsentStatus
	)
	err := d.pool.QueryRow(d.ctx, `SELECT status, marketing_consent FROM contacts.contacts WHERE tenant_id = $1 AND email = $2`,
		d.tenant, email).Scan(&st, &cs)
	if err != nil && d.count(t, `SELECT count(*) FROM contacts.contacts WHERE tenant_id = $1 AND email = $2`, d.tenant, email) == 0 {
		return "", ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return st, cs
}

func TestAltaConSuppressionContraLaBase(t *testing.T) {
	d := setupAdmission(t)
	unsubscribedAt := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	for _, email := range []string{"baja@example.com", "baja-api@example.com", "baja-form@example.com"} {
		d.sup.causes[email] = []domain.ActiveCause{{Cause: domain.CauseUnsubscribe, RegisteredAt: unsubscribedAt}}
	}
	d.sup.causes["manual@example.com"] = []domain.ActiveCause{{Cause: domain.CauseManual, RegisteredAt: unsubscribedAt}}

	if _, err := d.uc.CreateContact(d.ctx, d.tenant, CreateContactInput{Email: "baja@example.com"}); err != nil {
		t.Fatal(err)
	}
	if st, cs := d.stored(t, "baja@example.com"); st != domain.StatusUnsubscribed || cs != domain.ConsentNone {
		t.Fatalf("la baja entra unsubscribed sin evidencia: %s/%s", st, cs)
	}
	manual, err := d.uc.CreateContact(d.ctx, d.tenant, CreateContactInput{
		Email: "manual@example.com", Consent: &ConsentInput{Status: "granted", Method: "api", Source: "crm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st, cs := d.stored(t, "manual@example.com"); st != domain.StatusExcluded || cs != domain.ConsentGranted {
		t.Fatalf("la exclusion manual entra excluded con su consentimiento: %s/%s", st, cs)
	}
	if sendable, err := d.uc.SendableContacts(d.ctx, d.tenant, []uuid.UUID{manual.ID}); err != nil || len(sendable) != 0 {
		t.Fatalf("un excluded no es enviable aunque consintiera: %+v %v", sendable, err)
	}

	_, err = d.uc.CreateContact(d.ctx, d.tenant, CreateContactInput{
		Email: "baja-api@example.com", Consent: &ConsentInput{Status: "granted", Method: "api", Source: "crm"},
	})
	if !errors.Is(err, domain.ErrResubscribeRequiresOptIn) {
		t.Fatalf("consentimiento sin prueba sobre una baja: %v", err)
	}
	if st, _ := d.stored(t, "baja-api@example.com"); st != "" {
		t.Fatal("el rechazo no deja el contacto")
	}

	back, err := d.uc.CreateContact(d.ctx, d.tenant, CreateContactInput{
		Email:   "baja-form@example.com",
		Consent: &ConsentInput{Status: "granted", Method: "form", Source: "https://example.com/alta", IP: "203.0.113.7"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st, cs := d.stored(t, "baja-form@example.com"); st != domain.StatusActive || cs != domain.ConsentGranted {
		t.Fatalf("el formulario con ip lo reactiva: %s/%s", st, cs)
	}
	var consentedAt string
	if err := d.pool.QueryRow(d.ctx, `SELECT payload->'data'->>'consented_at' FROM platform.event_outbox
		WHERE tenant_id = $1 AND subject = 'contacts.contact.resubscribed' AND payload->'data'->>'email' = $2`,
		d.tenant, back.Email).Scan(&consentedAt); err != nil || consentedAt == "" {
		t.Fatalf("la resuscripcion queda en la outbox con su hora: %q %v", consentedAt, err)
	}

	d.sup.err = errors.New("suppression: status 503")
	if _, err := d.uc.CreateContact(d.ctx, d.tenant, CreateContactInput{Email: "caido@example.com"}); !errors.Is(err, ErrSuppressionUnavailable) {
		t.Fatalf("sin suppression: %v", err)
	}
	if st, _ := d.stored(t, "caido@example.com"); st != "" {
		t.Fatal("sin suppression no se escribe el contacto")
	}
}

// Una importacion de 1001 filas: tres consultas (500, 500, 1), cada contacto con el estado
// de su lote, consentimiento solo para los active y el recuento de excluidos en el rastro.
func TestImportacionConSuppressionContraLaBase(t *testing.T) {
	d := setupAdmission(t)
	email := func(i int) string { return fmt.Sprintf("imp%04d@example.com", i) }
	excluded := map[int]domain.Status{
		0: domain.StatusUnsubscribed, importBatchSize - 1: domain.StatusUnsubscribed,
		importBatchSize: domain.StatusBounced, maxCheckEmails: domain.StatusExcluded,
	}
	causeOf := map[domain.Status]domain.SuppressionCause{
		domain.StatusUnsubscribed: domain.CauseUnsubscribe, domain.StatusBounced: domain.CauseHardBounce,
		domain.StatusExcluded: domain.CauseManual,
	}
	for i, st := range excluded {
		d.sup.causes[email(i)] = []domain.ActiveCause{{Cause: causeOf[st]}}
	}
	rows := make([]ImportRow, maxCheckEmails+1)
	for i := range rows {
		rows[i] = ImportRow{Email: email(i)}
	}
	imp, err := d.uc.Import(d.ctx, d.tenant, ImportInput{Rows: rows, GrantConsent: true, ConsentBasis: "contrato", CreatedBy: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.sup.batchSizes, []int{importBatchSize, importBatchSize, 1}) {
		t.Fatalf("consultas: %v", d.sup.batchSizes)
	}
	for i, want := range excluded {
		if st, cs := d.stored(t, email(i)); st != want || cs != domain.ConsentNone {
			t.Errorf("%s: %s/%s, se esperaba %s sin consentimiento", email(i), st, cs, want)
		}
	}
	if n := d.count(t, `SELECT count(*) FROM contacts.contacts WHERE tenant_id = $1 AND status = 'active' AND marketing_consent = 'granted'`, d.tenant); n != len(rows)-len(excluded) {
		t.Fatalf("enviables: %d", n)
	}
	if n := d.count(t, `SELECT count(*) FROM contacts.consents WHERE tenant_id = $1 AND status <> 'granted'`, d.tenant); n != 0 {
		t.Fatalf("ninguna revocacion por la baja: %d", n)
	}
	saved, err := d.uc.GetImport(d.ctx, d.tenant, imp.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[domain.Status]int{domain.StatusUnsubscribed: 2, domain.StatusBounced: 1, domain.StatusExcluded: 1}
	if saved.Created != len(rows) || !reflect.DeepEqual(saved.Suppressed, want) {
		t.Fatalf("rastro: %+v", saved)
	}
}

func TestImportacionConSuppressionCaidoContraLaBase(t *testing.T) {
	d := setupAdmission(t)
	rows := make([]ImportRow, maxCheckEmails)
	for i := range rows {
		rows[i] = ImportRow{Email: fmt.Sprintf("caido%04d@example.com", i)}
	}
	d.sup.failFromBatch = 2
	if _, err := d.uc.Import(d.ctx, d.tenant, ImportInput{Rows: rows, CreatedBy: uuid.New()}); !errors.Is(err, ErrSuppressionUnavailable) {
		t.Fatalf("suppression cae a mitad: %v", err)
	}
	if n := d.count(t, `SELECT count(*) FROM contacts.contacts WHERE tenant_id = $1`, d.tenant); n != importBatchSize {
		t.Fatalf("solo el lote comprobado: %d", n)
	}
	if n := d.count(t, `SELECT count(*) FROM contacts.imports WHERE tenant_id = $1 AND status = 'failed' AND created = $2`, d.tenant, importBatchSize); n != 1 {
		t.Fatalf("rastro failed con lo confirmado: %d", n)
	}
}
