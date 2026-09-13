package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/alonsosss/corforce-email/services/suppression/internal/ports"
	"github.com/google/uuid"
)

func TestAddNormalizaYValidaLaDireccion(t *testing.T) {
	f := newFixture()
	ctx := context.Background()

	a, added, err := f.uc.Add(ctx, f.tenant, AddInput{Email: "  Ana.Perez@Example.COM ", Reason: domain.ReasonManual, Source: "api"})
	if err != nil || !added {
		t.Fatalf("Add: err=%v added=%v", err, added)
	}
	if a.Email != "ana.perez@example.com" {
		t.Fatalf("la direccion no se normalizo: %q", a.Email)
	}

	for _, bad := range []string{"", "sin-arroba", "a@b", "con espacio@example.com", "@example.com"} {
		if _, _, err := f.uc.Add(ctx, f.tenant, AddInput{Email: bad, Reason: domain.ReasonManual}); !errors.Is(err, domain.ErrInvalidEmail) {
			t.Errorf("%q: se esperaba ErrInvalidEmail, hubo %v", bad, err)
		}
	}
	if _, _, err := f.uc.Add(ctx, f.tenant, AddInput{Email: "ok@example.com", Reason: "soft_bounce"}); !errors.Is(err, domain.ErrInvalidReason) {
		t.Fatalf("se esperaba ErrInvalidReason, hubo %v", err)
	}
}

func TestAddGuardaCadaCausaPorSeparado(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	const email = "cliente@example.com"

	exp := f.now.Add(24 * time.Hour)
	if _, err := f.uc.CreateManual(ctx, f.tenant, CreateManualInput{Email: email, Reason: domain.ReasonManual, ExpiresAt: &exp}); err != nil {
		t.Fatal(err)
	}

	// Un rebote duro entra como causa propia: la exclusion manual temporal sigue con su
	// caducidad y la direccion pasa a tener el rebote como causa principal.
	a, added, err := f.uc.Add(ctx, f.tenant, AddInput{Email: email, Reason: domain.ReasonHardBounce, Source: "ses", Detail: "5.1.1"})
	if err != nil || !added {
		t.Fatalf("hard_bounce: err=%v added=%v", err, added)
	}
	if a.Reason != domain.ReasonHardBounce || a.Source != "ses" || a.ExpiresAt != nil {
		t.Fatalf("causa principal: %+v", a.Entry)
	}
	if !reflect.DeepEqual(a.Reasons, []domain.Reason{domain.ReasonHardBounce, domain.ReasonManual}) || len(a.Causes) != 2 {
		t.Fatalf("causas: reasons=%v causes=%d", a.Reasons, len(a.Causes))
	}
	if m := f.entries.find(f.tenant, email, domain.ReasonManual); m == nil || m.ExpiresAt == nil || !m.ExpiresAt.Equal(exp) {
		t.Fatalf("la exclusion manual conserva su caducidad: %+v", m)
	}

	// Una baja posterior tambien se guarda, aunque sea menos grave que el rebote.
	a, added, err = f.uc.Add(ctx, f.tenant, AddInput{Email: email, Reason: domain.ReasonUnsubscribe, Source: "transactional"})
	if err != nil || !added || a.Reason != domain.ReasonHardBounce {
		t.Fatalf("unsubscribe sobre hard_bounce: err=%v added=%v reason=%s", err, added, a.Reason)
	}
	want := []domain.Reason{domain.ReasonHardBounce, domain.ReasonUnsubscribe, domain.ReasonManual}
	if !reflect.DeepEqual(a.Reasons, want) {
		t.Fatalf("reasons=%v, se esperaba %v", a.Reasons, want)
	}

	// La misma causa es idempotente.
	if _, added, _ = f.uc.Add(ctx, f.tenant, AddInput{Email: email, Reason: domain.ReasonHardBounce, Source: "ses"}); added {
		t.Fatal("repetir la misma causa no debe contar como alta")
	}

	a, added, _ = f.uc.Add(ctx, f.tenant, AddInput{Email: email, Reason: domain.ReasonComplaint, Source: "ses"})
	if !added || a.Reason != domain.ReasonComplaint || len(a.Reasons) != 4 {
		t.Fatalf("complaint: added=%v reason=%s reasons=%v", added, a.Reason, a.Reasons)
	}

	if len(f.entries.entries) != 4 {
		t.Fatalf("una fila por causa: hay %d", len(f.entries.entries))
	}
	wantEvents := []string{
		"added|cliente@example.com|manual|manual",
		"added|cliente@example.com|hard_bounce|hard_bounce,manual",
		"added|cliente@example.com|unsubscribe|hard_bounce,unsubscribe,manual",
		"added|cliente@example.com|complaint|complaint,hard_bounce,unsubscribe,manual",
	}
	if !reflect.DeepEqual(f.events.events, wantEvents) {
		t.Fatalf("eventos:\n%v\nse esperaba:\n%v", f.events.events, wantEvents)
	}
	if len(f.entries.locks) == 0 {
		t.Fatal("cada alta bloquea la direccion")
	}
}

// El caso que motivo el modelo por causas: la baja no absorbe la exclusion manual, ni al
// entrar ni al levantarse.
func TestBajaYExclusionManualConviven(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	const email = "ana@example.com"

	if _, _, err := f.uc.Add(ctx, f.tenant, AddInput{Email: email, Reason: domain.ReasonUnsubscribe, Source: "transactional"}); err != nil {
		t.Fatal(err)
	}
	a, err := f.uc.CreateManual(ctx, f.tenant, CreateManualInput{Email: email, Reason: domain.ReasonManual, Detail: "cliente moroso"})
	if err != nil {
		t.Fatalf("la exclusion manual sobre una baja debe registrarse: %v", err)
	}
	if a.Reason != domain.ReasonUnsubscribe || !reflect.DeepEqual(a.Reasons, []domain.Reason{domain.ReasonUnsubscribe, domain.ReasonManual}) {
		t.Fatalf("direccion: reason=%s reasons=%v", a.Reason, a.Reasons)
	}
	got, err := f.uc.Check(ctx, f.tenant, []string{email})
	if err != nil || len(got) != 1 || got[0].Reason != domain.ReasonUnsubscribe ||
		!reflect.DeepEqual(got[0].Reasons, []domain.Reason{domain.ReasonUnsubscribe, domain.ReasonManual}) {
		t.Fatalf("consulta previa: %+v err=%v", got, err)
	}

	// El nuevo consentimiento levanta solo la baja: la exclusion manual sigue bloqueando.
	if removed, err := f.uc.Resubscribe(ctx, f.tenant, email); err != nil || !removed {
		t.Fatalf("resuscripcion: removed=%v err=%v", removed, err)
	}
	got, _ = f.uc.Check(ctx, f.tenant, []string{email})
	if len(got) != 1 || got[0].Reason != domain.ReasonManual || !reflect.DeepEqual(got[0].Reasons, []domain.Reason{domain.ReasonManual}) {
		t.Fatalf("tras la resuscripcion queda la manual: %+v", got)
	}
	last := f.events.events[len(f.events.events)-1]
	if last != "removed|ana@example.com|unsubscribe|manual" {
		t.Fatalf("el evento de retirada lleva la causa retirada y las que quedan: %s", last)
	}
}

func TestRemoveQuitaSoloEsaCausa(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	const email = "eva@example.com"

	baja := f.cause(email, domain.ReasonUnsubscribe, nil)
	manual := f.cause(email, domain.ReasonManual, nil)
	rebote := f.cause(email, domain.ReasonHardBounce, nil)

	if err := f.uc.Remove(ctx, f.tenant, manual.ID); err != nil {
		t.Fatalf("retirar la manual: %v", err)
	}
	if got := f.entries.reasonsOf(f.tenant, email); !reflect.DeepEqual(got, []string{"hard_bounce", "unsubscribe"}) {
		t.Fatalf("solo se retira la manual: %v", got)
	}
	if err := f.uc.Remove(ctx, f.tenant, baja.ID); !errors.Is(err, domain.ErrUnsubscribeProtected) {
		t.Fatalf("la baja no se retira por API aunque haya otras causas: %v", err)
	}
	if err := f.uc.Remove(ctx, f.tenant, rebote.ID); err != nil {
		t.Fatalf("retirar el rebote: %v", err)
	}
	got, _ := f.uc.Check(ctx, f.tenant, []string{email})
	if len(got) != 1 || got[0].Reason != domain.ReasonUnsubscribe {
		t.Fatalf("la baja sigue bloqueando: %+v", got)
	}
	want := []string{"removed|eva@example.com|manual|hard_bounce,unsubscribe", "removed|eva@example.com|hard_bounce|unsubscribe"}
	if !reflect.DeepEqual(f.events.events, want) {
		t.Fatalf("eventos: %v", f.events.events)
	}
}

func TestCausaManualCaducadaSeReactiva(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	past := f.now.Add(-time.Hour)
	f.cause("caducada@example.com", domain.ReasonManual, &past)

	if got, _ := f.uc.Check(ctx, f.tenant, []string{"caducada@example.com"}); len(got) != 0 {
		t.Fatalf("una manual caducada no bloquea: %+v", got)
	}
	// Una nueva exclusion manual renueva la caducada en vez de chocar con ella.
	future := f.now.Add(time.Hour)
	a, err := f.uc.CreateManual(ctx, f.tenant, CreateManualInput{Email: "caducada@example.com", Reason: domain.ReasonManual, Detail: "otra vez", ExpiresAt: &future})
	if err != nil || a.ExpiresAt == nil || !a.ExpiresAt.Equal(future) || a.Detail != "otra vez" || len(f.entries.entries) != 1 {
		t.Fatalf("renovacion: %+v err=%v filas=%d", a, err, len(f.entries.entries))
	}
	if _, err := f.uc.CreateManual(ctx, f.tenant, CreateManualInput{Email: "caducada@example.com", Reason: domain.ReasonManual}); !errors.Is(err, domain.ErrEntryAlreadyExists) {
		t.Fatalf("una manual vigente es conflicto: %v", err)
	}

	// Por la via interna, la manual caducada se reactiva sin caducidad.
	f.now = future.Add(time.Minute)
	a, added, err := f.uc.Add(ctx, f.tenant, AddInput{Email: "caducada@example.com", Reason: domain.ReasonManual, Source: "campaign"})
	if err != nil || !added || a.ExpiresAt != nil || a.Source != "campaign" || !a.Active() {
		t.Fatalf("reactivacion interna: %+v added=%v err=%v", a, added, err)
	}
}

func TestCheckRespetaExpiresAt(t *testing.T) {
	f := newFixture()
	ctx := context.Background()

	past := f.now.Add(-time.Minute)
	future := f.now.Add(time.Minute)
	f.cause("caducada@example.com", domain.ReasonManual, &past)
	f.cause("vigente@example.com", domain.ReasonManual, &future)
	f.cause("rebote@example.com", domain.ReasonHardBounce, nil)
	f.cause("mixta@example.com", domain.ReasonManual, &past)
	f.cause("mixta@example.com", domain.ReasonUnsubscribe, nil)
	f.entries.entries = append(f.entries.entries, &domain.Entry{ID: uuid.New(), TenantID: uuid.New(), Email: "otra-empresa@example.com", Reason: domain.ReasonComplaint})

	got, err := f.uc.Check(ctx, f.tenant, []string{
		"Caducada@Example.com", "VIGENTE@example.com", "rebote@example.com", "mixta@example.com",
		"otra-empresa@example.com", "libre@example.com", "no-es-email",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]domain.Reason{
		"vigente@example.com": {domain.ReasonManual},
		"rebote@example.com":  {domain.ReasonHardBounce},
		"mixta@example.com":   {domain.ReasonUnsubscribe},
	}
	if len(got) != len(want) {
		t.Fatalf("suprimidas: %+v", got)
	}
	for _, s := range got {
		if !reflect.DeepEqual(want[s.Email], s.Reasons) || s.Reason != s.Reasons[0] {
			t.Errorf("%s: reason=%s reasons=%v", s.Email, s.Reason, s.Reasons)
		}
	}

	if _, err := f.uc.Check(ctx, f.tenant, make([]string, MaxCheckEmails+1)); !errors.Is(err, domain.ErrTooManyEmails) {
		t.Fatalf("se esperaba ErrTooManyEmails, hubo %v", err)
	}
}

func TestRemoveRechazaUnaBajaYAuditaElResto(t *testing.T) {
	f := newFixture()
	ctx := context.Background()

	baja := f.cause("baja@example.com", domain.ReasonUnsubscribe, nil)
	queja := f.cause("queja@example.com", domain.ReasonComplaint, nil)

	if err := f.uc.Remove(ctx, f.tenant, baja.ID); !errors.Is(err, domain.ErrUnsubscribeProtected) {
		t.Fatalf("borrar una baja: se esperaba ErrUnsubscribeProtected, hubo %v", err)
	}
	if err := f.uc.Remove(ctx, f.tenant, queja.ID); err != nil {
		t.Fatalf("borrar una queja: %v", err)
	}
	if err := f.uc.Remove(ctx, f.tenant, queja.ID); !errors.Is(err, domain.ErrEntryNotFound) {
		t.Fatalf("segundo borrado: se esperaba ErrEntryNotFound, hubo %v", err)
	}
	if len(f.events.events) != 1 || f.events.events[0] != "removed|queja@example.com|complaint|" {
		t.Fatalf("eventos: %v", f.events.events)
	}
	// Otra empresa no ve la fila.
	if err := f.uc.Remove(ctx, uuid.New(), baja.ID); !errors.Is(err, domain.ErrEntryNotFound) {
		t.Fatalf("otra empresa: se esperaba ErrEntryNotFound, hubo %v", err)
	}
}

func TestResubscribeSoloLevantaLaBaja(t *testing.T) {
	f := newFixture()
	ctx := context.Background()

	f.cause("baja@example.com", domain.ReasonUnsubscribe, nil)
	f.cause("rebote@example.com", domain.ReasonHardBounce, nil)
	if removed, err := f.uc.Resubscribe(ctx, f.tenant, "Baja@example.com"); err != nil || !removed {
		t.Fatalf("baja: removed=%v err=%v", removed, err)
	}
	if removed, err := f.uc.Resubscribe(ctx, f.tenant, "baja@example.com"); err != nil || removed {
		t.Fatalf("segunda vez debe ser idempotente: removed=%v err=%v", removed, err)
	}
	if removed, err := f.uc.Resubscribe(ctx, f.tenant, "rebote@example.com"); err != nil || removed {
		t.Fatalf("un rebote no se levanta por consentimiento: removed=%v err=%v", removed, err)
	}
	if len(f.entries.entries) != 1 || f.entries.entries[0].Reason != domain.ReasonHardBounce {
		t.Fatalf("estado final: %+v", f.entries.entries)
	}
}

func TestIngestIgnoraRebotesTransitorios(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	msg := uuid.New()

	res, err := f.uc.Ingest(ctx, DeliveryEvent{Subject: SubjectEmailBounced, TenantID: f.tenant, Email: "lleno@example.com", BounceType: "transient", MessageID: &msg})
	if err != nil || !res.Ignored {
		t.Fatalf("transient: err=%v res=%+v", err, res)
	}
	if len(f.entries.entries) != 0 {
		t.Fatal("un rebote transitorio no debe suprimir")
	}

	res, err = f.uc.Ingest(ctx, DeliveryEvent{Subject: SubjectEmailBounced, TenantID: f.tenant, Email: "Inexistente@example.com", BounceType: "permanent", Detail: "5.1.1", MessageID: &msg})
	if err != nil || !res.Added {
		t.Fatalf("permanent: err=%v res=%+v", err, res)
	}
	e := f.entries.entries[0]
	if e.Reason != domain.ReasonHardBounce || e.Source != SourceSES || e.MessageID == nil || *e.MessageID != msg || e.Email != "inexistente@example.com" {
		t.Fatalf("fila: %+v", e)
	}

	// Reentrega del mismo evento: sin cambios.
	if res, _ = f.uc.Ingest(ctx, DeliveryEvent{Subject: SubjectEmailBounced, TenantID: f.tenant, Email: "inexistente@example.com", BounceType: "permanent", MessageID: &msg}); res.Added || res.Ignored {
		t.Fatalf("reentrega: %+v", res)
	}

	res, err = f.uc.Ingest(ctx, DeliveryEvent{Subject: SubjectEmailComplained, TenantID: f.tenant, Email: "inexistente@example.com"})
	if err != nil || !res.Added || len(f.entries.entries) != 2 {
		t.Fatalf("complaint: err=%v res=%+v filas=%d", err, res, len(f.entries.entries))
	}
	if got, _ := f.uc.Check(ctx, f.tenant, []string{"inexistente@example.com"}); len(got) != 1 || got[0].Reason != domain.ReasonComplaint {
		t.Fatalf("la queja es la causa principal: %+v", got)
	}

	if res, _ = f.uc.Ingest(ctx, DeliveryEvent{Subject: "transactional.email.delivered", TenantID: f.tenant, Email: "x@example.com"}); !res.Ignored {
		t.Fatal("un subject ajeno se ignora")
	}
	if _, err := f.uc.Ingest(ctx, DeliveryEvent{Subject: SubjectEmailComplained, TenantID: f.tenant, Email: "roto"}); !IsInputError(err) {
		t.Fatalf("direccion invalida debe ser error de entrada, hubo %v", err)
	}
}

func TestCreateManualSoloManualYConCaducidadFutura(t *testing.T) {
	f := newFixture()
	ctx := context.Background()

	if _, err := f.uc.CreateManual(ctx, f.tenant, CreateManualInput{Email: "a@example.com", Reason: domain.ReasonHardBounce}); !errors.Is(err, domain.ErrManualOnly) {
		t.Fatalf("se esperaba ErrManualOnly, hubo %v", err)
	}
	past := f.now.Add(-time.Second)
	if _, err := f.uc.CreateManual(ctx, f.tenant, CreateManualInput{Email: "a@example.com", Reason: domain.ReasonManual, ExpiresAt: &past}); !errors.Is(err, domain.ErrExpiryInPast) {
		t.Fatalf("se esperaba ErrExpiryInPast, hubo %v", err)
	}
	if _, err := f.uc.CreateManual(ctx, f.tenant, CreateManualInput{Email: "a@example.com", Reason: domain.ReasonManual}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.CreateManual(ctx, f.tenant, CreateManualInput{Email: "A@example.com", Reason: domain.ReasonManual}); !errors.Is(err, domain.ErrEntryAlreadyExists) {
		t.Fatalf("se esperaba ErrEntryAlreadyExists, hubo %v", err)
	}
}

func TestImportCuentaOmitidasYDejaRastro(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	user := uuid.New()

	f.cause("ya@example.com", domain.ReasonComplaint, nil)
	f.cause("manual@example.com", domain.ReasonManual, nil)
	imp, err := f.uc.Import(ctx, f.tenant, ImportInput{
		Emails:    []string{"Nueva@example.com", "nueva@example.com", "ya@example.com", "manual@example.com", "invalida", "otra@example.com"},
		Reason:    domain.ReasonManual,
		CreatedBy: user,
	})
	if err != nil {
		t.Fatal(err)
	}
	// ya@example.com tenia una queja: gana la causa manual aparte. manual@example.com ya
	// la tenia vigente y se omite, igual que la repetida y la invalida.
	if imp.Total != 6 || imp.Added != 3 || imp.Skipped != 3 {
		t.Fatalf("conteo: %+v", imp)
	}
	if len(f.imports.imports) != 1 || f.imports.imports[0].CreatedBy != user {
		t.Fatalf("rastro: %+v", f.imports.imports)
	}
	if got := f.entries.reasonsOf(f.tenant, "ya@example.com"); !reflect.DeepEqual(got, []string{"complaint", "manual"}) {
		t.Fatalf("la carga suma la manual sin tocar la queja: %v", got)
	}
	want := []string{
		"added|nueva@example.com|manual|manual",
		"added|ya@example.com|manual|complaint,manual",
		"added|otra@example.com|manual|manual",
	}
	if !reflect.DeepEqual(f.events.events, want) {
		t.Fatalf("eventos: %v", f.events.events)
	}

	if _, err := f.uc.Import(ctx, f.tenant, ImportInput{Reason: domain.ReasonManual, CreatedBy: user}); !errors.Is(err, domain.ErrNoEmails) {
		t.Fatalf("se esperaba ErrNoEmails, hubo %v", err)
	}
	if _, err := f.uc.Import(ctx, f.tenant, ImportInput{Emails: make([]string, MaxImportEmails+1), Reason: domain.ReasonManual, CreatedBy: user}); !errors.Is(err, domain.ErrTooManyEmails) {
		t.Fatalf("se esperaba ErrTooManyEmails, hubo %v", err)
	}
}

func TestStatsCuentaDireccionesPorCausaPrincipal(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	past := f.now.Add(-time.Hour)
	f.cause("a@example.com", domain.ReasonComplaint, nil)
	f.cause("b@example.com", domain.ReasonComplaint, nil)
	f.cause("b@example.com", domain.ReasonManual, nil)
	f.cause("c@example.com", domain.ReasonManual, &past)
	f.cause("d@example.com", domain.ReasonUnsubscribe, nil)
	f.cause("d@example.com", domain.ReasonManual, &past)
	s, err := f.uc.Stats(ctx, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	if s.Total != 3 || s.ByReason["complaint"] != 2 || s.ByReason["unsubscribe"] != 1 || s.ByReason["manual"] != 0 || len(s.ByReason) != len(domain.Reasons()) {
		t.Fatalf("stats: %+v", s)
	}
}

func TestListDevuelveDireccionesConTodasSusCausas(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	f.cause("ana@example.com", domain.ReasonUnsubscribe, nil)
	manual := f.cause("ana@example.com", domain.ReasonManual, nil)
	f.cause("eva@example.com", domain.ReasonManual, nil)

	list, total, err := f.uc.List(ctx, f.tenant, ports.ListFilter{Page: 1, PerPage: 20})
	if err != nil || total != 2 || len(list) != 2 {
		t.Fatalf("una fila por direccion: total=%d n=%d err=%v", total, len(list), err)
	}
	for _, a := range list {
		if a.Email == "ana@example.com" && (a.Reason != domain.ReasonUnsubscribe || len(a.Causes) != 2 || len(a.Reasons) != 2) {
			t.Fatalf("ana: %+v", a)
		}
	}
	// El filtro por causa mira la principal: ana (baja) no sale entre las manuales.
	list, total, _ = f.uc.List(ctx, f.tenant, ports.ListFilter{Reason: domain.ReasonManual, Page: 1, PerPage: 20})
	if total != 1 || list[0].Email != "eva@example.com" {
		t.Fatalf("filtro por causa principal: %+v", list)
	}
	if _, _, err := f.uc.List(ctx, f.tenant, ports.ListFilter{Reason: "soft", Page: 1, PerPage: 20}); !errors.Is(err, domain.ErrInvalidReason) {
		t.Fatalf("se esperaba ErrInvalidReason, hubo %v", err)
	}
	// La consulta de una causa devuelve su direccion completa.
	a, err := f.uc.Get(ctx, f.tenant, manual.ID)
	if err != nil || a.Email != "ana@example.com" || a.Reason != domain.ReasonUnsubscribe || len(a.Causes) != 2 {
		t.Fatalf("Get: %+v err=%v", a, err)
	}
}
