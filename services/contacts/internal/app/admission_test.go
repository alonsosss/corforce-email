package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

// maxCheckEmails es el tope de direcciones por consulta de suppression (max_check_emails en
// GET /suppression/meta).
const maxCheckEmails = 1000

// El alta por API entra con el estado de las causas vigentes de la direccion (la mas grave
// si hay varias), consultadas con la direccion normalizada. Sin consentimiento declarado no
// se escribe evidencia, tampoco una revocacion por la baja.
func TestAltaEntraConElEstadoDeSuppression(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	cases := []struct {
		email  string
		causes []domain.SuppressionCause
		want   domain.Status
	}{
		{"libre@example.com", nil, domain.StatusActive},
		{"queja@example.com", []domain.SuppressionCause{domain.CauseComplaint}, domain.StatusComplained},
		{"rebote@example.com", []domain.SuppressionCause{domain.CauseHardBounce}, domain.StatusBounced},
		{"baja@example.com", []domain.SuppressionCause{domain.CauseUnsubscribe}, domain.StatusUnsubscribed},
		{"invalida@example.com", []domain.SuppressionCause{domain.CauseInvalid}, domain.StatusInvalid},
		{"manual@example.com", []domain.SuppressionCause{domain.CauseManual}, domain.StatusExcluded},
		{"varias@example.com", []domain.SuppressionCause{domain.CauseManual, domain.CauseUnsubscribe, domain.CauseInvalid}, domain.StatusUnsubscribed},
	}
	for _, tc := range cases {
		f.suppressed(tc.email, tc.causes...)
		c, err := f.uc.CreateContact(ctx, f.tenant, CreateContactInput{Email: strings.ToUpper(tc.email)})
		if err != nil {
			t.Fatalf("%s: %v", tc.email, err)
		}
		if c.Status != tc.want || f.contact(t, c.ID).Status != tc.want || c.ConsentStatus != domain.ConsentNone {
			t.Errorf("%s: %s/%s, se esperaba %s sin consentimiento", tc.email, c.Status, c.ConsentStatus, tc.want)
		}
	}
	if len(f.s.consents) != 0 {
		t.Fatalf("sin consentimiento declarado no hay evidencia: %+v", f.s.consents)
	}
	if want := []int{1, 1, 1, 1, 1, 1, 1}; !reflect.DeepEqual(f.sup.batchSizes, want) {
		t.Fatalf("una consulta por alta: %v", f.sup.batchSizes)
	}
	if f.ev.count("contact.created|") != len(cases) || len(f.ev.events) != len(cases) {
		t.Fatalf("un contact.created por alta y nada mas: %v", f.ev.events)
	}
}

// A quien se dio de baja no lo vuelve a suscribir un alta con consentimiento sin prueba
// (CheckGrant): se rechaza sin escribir nada. Un formulario con ip si lo devuelve, como en
// RecordConsent, y publica contacts.contact.resubscribed para que suppression retire la baja.
func TestAltaConConsentimientoSobreUnaBaja(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.suppressed("baja@example.com", domain.CauseUnsubscribe)

	for _, consent := range []ConsentInput{
		{Status: "granted", Method: "api", Source: "crm"},
		{Status: "granted", Method: "form", Source: "https://example.com/alta"},
	} {
		_, err := f.uc.CreateContact(ctx, f.tenant, CreateContactInput{Email: "baja@example.com", Consent: &consent})
		if !errors.Is(err, domain.ErrResubscribeRequiresOptIn) {
			t.Fatalf("%s sin ip: %v", consent.Method, err)
		}
	}
	if len(f.s.contacts) != 0 || len(f.s.consents) != 0 || len(f.ev.events) != 0 {
		t.Fatalf("sin prueba no se escribe nada: %d contactos, %d consentimientos, %v", len(f.s.contacts), len(f.s.consents), f.ev.events)
	}

	c, err := f.uc.CreateContact(ctx, f.tenant, CreateContactInput{
		Email:   "baja@example.com",
		Consent: &ConsentInput{Status: "granted", Method: "form", Source: "https://example.com/alta", IP: "203.0.113.7"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.contact(t, c.ID); got.Status != domain.StatusActive || got.ConsentStatus != domain.ConsentGranted || !got.Sendable() {
		t.Fatalf("el formulario con ip lo reactiva: %+v", got)
	}
	want := []string{"contact.created|baja@example.com", "contact.updated|baja@example.com|status", "contact.resubscribed|baja@example.com", "consent.granted|form"}
	if !reflect.DeepEqual(f.ev.events, want) {
		t.Fatalf("eventos %v, se esperaban %v", f.ev.events, want)
	}
	if len(f.s.consents) != 1 || !reflect.DeepEqual(f.ev.consentedAt, []time.Time{f.s.consents[0].OccurredAt}) {
		t.Fatalf("la resuscripcion lleva la hora del consentimiento: %v %+v", f.ev.consentedAt, f.s.consents)
	}
}

// Las demas exclusiones no las pidio la persona: el alta registra su consentimiento, el
// contacto entra excluido y no es enviable mientras la causa siga vigente.
func TestAltaConConsentimientoSobreOtrasExclusiones(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for cause, want := range map[domain.SuppressionCause]domain.Status{
		domain.CauseManual: domain.StatusExcluded, domain.CauseInvalid: domain.StatusInvalid,
		domain.CauseHardBounce: domain.StatusBounced, domain.CauseComplaint: domain.StatusComplained,
	} {
		email := string(cause) + "@example.com"
		f.suppressed(email, cause)
		c, err := f.uc.CreateContact(ctx, f.tenant, CreateContactInput{Email: email, Consent: &ConsentInput{Status: "granted", Method: "api", Source: "crm"}})
		if err != nil {
			t.Fatalf("%s: %v", cause, err)
		}
		if got := f.contact(t, c.ID); got.Status != want || got.ConsentStatus != domain.ConsentGranted || got.Sendable() {
			t.Errorf("%s: %+v, se esperaba %s con el consentimiento y sin poder enviarle", cause, got, want)
		}
	}
	if f.ev.count("contact.resubscribed|") != 0 || f.ev.count("contact.updated|") != 0 {
		t.Fatalf("nada que reactivar: %v", f.ev.events)
	}
}

func TestAltaConSuppressionCaido(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.sup.err = errors.New("suppression: status 503")
	_, err := f.uc.CreateContact(ctx, f.tenant, CreateContactInput{
		Email: "ana@example.com", Consent: &ConsentInput{Status: "granted", Method: "api", Source: "crm"},
	})
	if !errors.Is(err, ErrSuppressionUnavailable) {
		t.Fatalf("sin suppression el alta falla: %v", err)
	}
	if len(f.s.contacts) != 0 || len(f.s.consents) != 0 || len(f.ev.events) != 0 {
		t.Fatal("y no escribe nada")
	}

	// Los errores de la peticion no dependen de suppression ni lo consultan.
	f.sup.batchSizes = nil
	if _, err := f.uc.CreateContact(ctx, f.tenant, CreateContactInput{Email: "no-es-email"}); !errors.Is(err, domain.ErrInvalidEmail) {
		t.Fatalf("direccion no valida: %v", err)
	}
	if len(f.sup.batchSizes) != 0 {
		t.Fatalf("una peticion invalida no consulta suppression: %v", f.sup.batchSizes)
	}

	f.uc.suppression = nil
	if _, err := f.uc.CreateContact(ctx, f.tenant, CreateContactInput{Email: "ana@example.com"}); !errors.Is(err, ErrSuppressionUnavailable) {
		t.Fatalf("sin lector de suppression tampoco se escribe: %v", err)
	}
}

// La importacion decide como el alta: cada contacto nuevo entra con el estado de sus causas
// vigentes y solo los que quedan active reciben el consentimiento declarado, asi que una
// fila con baja vigente no se vuelve enviable aunque la importacion declare consentimiento.
// A un existente no le cambia el estado (lo fijan sus eventos), pero una baja vigente que aun
// no refleja tambien le impide el consentimiento.
func TestImportacionEntraConElEstadoDeSuppression(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	list, err := f.uc.CreateList(ctx, f.tenant, "Feria", "")
	if err != nil {
		t.Fatal(err)
	}
	existeBaja := f.addContact(t, "existe-baja@example.com", domain.StatusActive, domain.ConsentNone)
	existeManual := f.addContact(t, "existe-manual@example.com", domain.StatusActive, domain.ConsentNone)
	existeLibre := f.addContact(t, "existe-libre@example.com", domain.StatusActive, domain.ConsentNone)
	f.suppressed(existeBaja.Email, domain.CauseUnsubscribe)
	f.suppressed(existeManual.Email, domain.CauseManual)
	want := map[string]domain.Status{
		"libre@example.com":    domain.StatusActive,
		"baja@example.com":     domain.StatusUnsubscribed,
		"rebote@example.com":   domain.StatusBounced,
		"queja@example.com":    domain.StatusComplained,
		"invalida@example.com": domain.StatusInvalid,
		"manual@example.com":   domain.StatusExcluded,
		"varias@example.com":   domain.StatusUnsubscribed,
	}
	f.suppressed("baja@example.com", domain.CauseUnsubscribe)
	f.suppressed("rebote@example.com", domain.CauseHardBounce)
	f.suppressed("queja@example.com", domain.CauseComplaint)
	f.suppressed("invalida@example.com", domain.CauseInvalid)
	f.suppressed("manual@example.com", domain.CauseManual)
	f.suppressed("varias@example.com", domain.CauseManual, domain.CauseUnsubscribe)

	rows := []ImportRow{{Email: existeBaja.Email}, {Email: existeManual.Email}, {Email: existeLibre.Email}}
	for email := range want {
		rows = append(rows, ImportRow{Email: strings.ToUpper(email)})
	}
	in := ImportInput{
		Rows: rows, ListID: &list.ID, UpdateExisting: true, GrantConsent: true,
		ConsentBasis: "Formulario de la feria, casilla marcada", CreatedBy: uuid.New(),
	}
	imp, err := f.uc.Import(ctx, f.tenant, in)
	if err != nil {
		t.Fatal(err)
	}
	if imp.Created != 7 || imp.Updated != 3 || imp.Skipped != 0 {
		t.Fatalf("conteos: %+v", imp)
	}
	wantSuppressed := map[domain.Status]int{
		domain.StatusUnsubscribed: 2, domain.StatusBounced: 1, domain.StatusComplained: 1,
		domain.StatusInvalid: 1, domain.StatusExcluded: 1,
	}
	if !reflect.DeepEqual(imp.Suppressed, wantSuppressed) || !reflect.DeepEqual(f.s.imports[0].Suppressed, wantSuppressed) {
		t.Fatalf("creados ya excluidos por estado: %v (rastro %v)", imp.Suppressed, f.s.imports[0].Suppressed)
	}
	granted := map[string]bool{}
	for _, cs := range f.s.consents {
		if cs.Status != domain.ConsentGranted || cs.Method != domain.MethodImport {
			t.Fatalf("solo evidencia de concesion por importacion, ninguna revocacion: %+v", cs)
		}
		granted[f.contact(t, cs.ContactID).Email] = true
	}
	wantGranted := map[string]bool{"libre@example.com": true, existeManual.Email: true, existeLibre.Email: true}
	if !reflect.DeepEqual(granted, wantGranted) {
		t.Fatalf("reciben el consentimiento %v, se esperaba %v", granted, wantGranted)
	}
	for _, c := range f.s.contacts {
		if st, isNew := want[c.Email]; isNew && c.Status != st {
			t.Errorf("%s entro como %s, se esperaba %s", c.Email, c.Status, st)
		}
	}
	for _, email := range []string{"baja@example.com", "varias@example.com"} {
		c, _ := (fakeContacts{f.s}).GetByEmailForUpdate(ctx, f.tenant, email)
		if c.Sendable() || c.ConsentStatus != domain.ConsentNone {
			t.Fatalf("una baja vigente no se vuelve enviable por declarar consentimiento: %+v", c)
		}
	}
	if got := f.contact(t, existeBaja.ID); got.Status != domain.StatusActive || got.ConsentStatus != domain.ConsentNone {
		t.Fatalf("el existente con la baja sin reflejar ni cambia de estado ni recibe consentimiento: %+v", got)
	}
	if l, _ := f.uc.GetList(ctx, f.tenant, list.ID); l.MemberCount != int64(len(rows)) {
		t.Fatalf("todas las filas procesadas entran en la lista: %d", l.MemberCount)
	}
	if len(f.ev.events) != 1 || f.ev.count("import.completed|") != 1 {
		t.Fatalf("un evento por importacion: %v", f.ev.events)
	}

	// Repetir la importacion no cambia nada: los que entraron excluidos ya existen y siguen
	// sin consentimiento, y los concedidos no se duplican.
	consents := len(f.s.consents)
	again, err := f.uc.Import(ctx, f.tenant, in)
	if err != nil {
		t.Fatal(err)
	}
	if again.Created != 0 || again.Updated != len(rows) || len(again.Suppressed) != 0 || len(f.s.consents) != consents {
		t.Fatalf("idempotente: %+v, %d consentimientos", again, len(f.s.consents))
	}
}

// Cada lote de 500 es una consulta, por debajo del tope de suppression, y cada contacto
// entra con las causas de su propio lote, tambien en los bordes.
func TestImportacionConsultaSuppressionPorLotes(t *testing.T) {
	for _, tc := range []struct {
		rows  int
		sizes []int
	}{
		{maxCheckEmails, []int{importBatchSize, importBatchSize}},
		{maxCheckEmails + 1, []int{importBatchSize, importBatchSize, 1}},
	} {
		f := newFixture(t)
		f.uc.cfg.ImportMaxRows = 2 * maxCheckEmails
		rows := make([]ImportRow, tc.rows)
		for i := range rows {
			rows[i] = ImportRow{Email: fmt.Sprintf("c%04d@example.com", i)}
		}
		bounced := map[string]bool{}
		for _, i := range []int{0, importBatchSize - 1, importBatchSize, maxCheckEmails - 1, tc.rows - 1} {
			email := fmt.Sprintf("c%04d@example.com", i)
			bounced[email] = true
			f.suppressed(email, domain.CauseHardBounce)
		}
		imp, err := f.uc.Import(context.Background(), f.tenant, ImportInput{Rows: rows, GrantConsent: true, ConsentBasis: "contrato", CreatedBy: uuid.New()})
		if err != nil {
			t.Fatalf("%d filas: %v", tc.rows, err)
		}
		if !reflect.DeepEqual(f.sup.batchSizes, tc.sizes) {
			t.Fatalf("%d filas: consultas %v, se esperaban %v", tc.rows, f.sup.batchSizes, tc.sizes)
		}
		for _, n := range f.sup.batchSizes {
			if n > maxCheckEmails {
				t.Fatalf("una consulta de %d supera el tope de suppression", n)
			}
		}
		if imp.Created != tc.rows || imp.Suppressed[domain.StatusBounced] != len(bounced) || len(f.s.consents) != tc.rows-len(bounced) {
			t.Fatalf("%d filas: %+v, %d consentimientos", tc.rows, imp, len(f.s.consents))
		}
		for _, c := range f.s.contacts {
			if bounced[c.Email] != (c.Status == domain.StatusBounced) || bounced[c.Email] == (c.ConsentStatus == domain.ConsentGranted) {
				t.Fatalf("%d filas: %s quedo %s/%s", tc.rows, c.Email, c.Status, c.ConsentStatus)
			}
		}
	}
}

// Sin suppression no se escribe ningun contacto sin comprobar. Si falla desde el principio
// no se escribe nada; si cae a mitad quedan los lotes ya comprobados, la importacion queda
// failed con lo que confirmo y repetirla completa lo que falto.
func TestImportacionConSuppressionCaido(t *testing.T) {
	ctx := context.Background()
	rows := make([]ImportRow, maxCheckEmails)
	for i := range rows {
		rows[i] = ImportRow{Email: fmt.Sprintf("c%04d@example.com", i)}
	}
	in := ImportInput{Rows: rows, GrantConsent: true, ConsentBasis: "contrato", CreatedBy: uuid.New()}

	f := newFixture(t)
	f.sup.err = errors.New("timeout")
	if _, err := f.uc.Import(ctx, f.tenant, in); !errors.Is(err, ErrSuppressionUnavailable) {
		t.Fatalf("sin suppression la importacion falla: %v", err)
	}
	if len(f.s.contacts) != 0 || len(f.s.consents) != 0 || f.ev.count("import.completed|") != 0 {
		t.Fatal("y no escribe ningun contacto")
	}
	if len(f.s.imports) != 1 || f.s.imports[0].Status != domain.ImportFailed || f.s.imports[0].Created != 0 {
		t.Fatalf("el rastro dice que fallo sin crear nada: %+v", f.s.imports)
	}

	f = newFixture(t)
	f.sup.failFromBatch = 2
	f.suppressed("c0010@example.com", domain.CauseHardBounce)
	if _, err := f.uc.Import(ctx, f.tenant, in); !errors.Is(err, ErrSuppressionUnavailable) {
		t.Fatalf("suppression cae a mitad: %v", err)
	}
	if len(f.s.contacts) != importBatchSize {
		t.Fatalf("solo el lote comprobado: %d contactos", len(f.s.contacts))
	}
	for _, c := range f.s.contacts {
		if c.Email >= fmt.Sprintf("c%04d@example.com", importBatchSize) {
			t.Fatalf("%s se escribio sin comprobar", c.Email)
		}
	}
	trail := f.s.imports[0]
	if trail.Status != domain.ImportFailed || trail.Created != importBatchSize || trail.Suppressed[domain.StatusBounced] != 1 {
		t.Fatalf("rastro de lo confirmado: %+v", trail)
	}

	f.sup.failFromBatch = 0
	imp, err := f.uc.Import(ctx, f.tenant, in)
	if err != nil {
		t.Fatal(err)
	}
	if imp.Status != domain.ImportCompleted || imp.Created != maxCheckEmails-importBatchSize || imp.Skipped != importBatchSize {
		t.Fatalf("el reintento crea lo que falto: %+v", imp)
	}
	if len(f.s.contacts) != maxCheckEmails || len(f.s.consents) != maxCheckEmails-1 {
		t.Fatalf("%d contactos, %d consentimientos", len(f.s.contacts), len(f.s.consents))
	}
}
