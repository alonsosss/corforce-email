package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
)

type vacTx struct{}

func (vacTx) InTx(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

type vacMailboxes struct {
	ports.MailboxRepository
	m *domain.Mailbox
}

func (f vacMailboxes) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.Mailbox, error) {
	if f.m != nil && f.m.TenantID == tenantID && f.m.ID == id {
		return f.m, nil
	}
	return nil, domain.ErrNotFound
}

type vacRetirements struct{ ports.RetirementRepository }

func (vacRetirements) HoldShared(context.Context, uuid.UUID) (bool, error) { return false, nil }

type vacRepo struct {
	saved *domain.VacationReply
}

func (f *vacRepo) ByUsername(context.Context, uuid.UUID, string) (*domain.VacationReply, error) {
	if f.saved == nil {
		return nil, domain.ErrNotFound
	}
	c := *f.saved
	return &c, nil
}
func (f *vacRepo) Upsert(_ context.Context, v *domain.VacationReply) error {
	c := *v
	f.saved = &c
	return nil
}
func (f *vacRepo) DeleteByUsername(context.Context, uuid.UUID, string) error { return nil }

type vacLocator struct {
	m *domain.Mailbox
	// pedido es el nombre con el que se pregunto: siempre normalizado.
	pedido string
}

func (f *vacLocator) Locate(_ context.Context, username string) (uuid.UUID, uuid.UUID, error) {
	f.pedido = username
	if f.m != nil && f.m.Username == username {
		return f.m.TenantID, f.m.ID, nil
	}
	return uuid.Nil, uuid.Nil, domain.ErrNotFound
}

func vacationServer(t *testing.T) (http.Handler, *vacRepo, *vacLocator, *domain.Mailbox) {
	t.Helper()
	m := &domain.Mailbox{ID: uuid.New(), TenantID: uuid.New(), Username: "ana@acme.test"}
	repo, loc := &vacRepo{}, &vacLocator{m: m}
	uc := app.New(app.Deps{Tx: vacTx{}, Mailboxes: vacMailboxes{m: m}, Retirements: vacRetirements{}, Vacation: repo, Locator: loc})
	return NewHandler(uc, authz.NewChecker("http://127.0.0.1:9", "")).Routes(), repo, loc, m
}

func doVacation(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type vacationEnvelope struct {
	Data  vacationResponse `json:"data"`
	Error *struct {
		Code string `json:"code"`
	} `json:"error"`
}

func decodeVacation(t *testing.T, rec *httptest.ResponseRecorder) vacationEnvelope {
	t.Helper()
	var env vacationEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("cuerpo ilegible %q: %v", rec.Body.String(), err)
	}
	return env
}

func TestRespuestaAutomaticaInternaLeeYEscribePorNombreDeBuzon(t *testing.T) {
	h, repo, loc, _ := vacationServer(t)
	rec := doVacation(h, http.MethodGet, "/internal/mail-directory/vacation?username=Ana@Acme.TEST", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	if env := decodeVacation(t, rec); env.Data.Enabled || env.Data.IntervalDays != 1 || env.Data.UpdatedAt != nil {
		t.Fatalf("un buzon sin configurar la ve desactivada: %+v", env.Data)
	}
	if lim := decodeVacation(t, rec).Data.Limits; lim.MessageMaxLength != domain.MaxVacationMessageRunes ||
		lim.SubjectMaxLength != domain.MaxVacationSubjectRunes || lim.IntervalMinDays != 1 || lim.IntervalMaxDays != 30 {
		t.Fatalf("la respuesta trae los topes del directorio: %+v", lim)
	}
	if loc.pedido != "ana@acme.test" {
		t.Fatalf("el nombre debe llegar normalizado: %q", loc.pedido)
	}

	body := `{"enabled":true,"subject":"Ausente","message":"Vuelvo el lunes.","interval_days":3,"starts_on":"2026-09-21","ends_on":"2026-09-30"}`
	rec = doVacation(h, http.MethodPut, "/internal/mail-directory/vacation?username=ana@acme.test", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	env := decodeVacation(t, rec)
	if !env.Data.Enabled || env.Data.Subject != "Ausente" || env.Data.IntervalDays != 3 ||
		env.Data.StartsOn == nil || *env.Data.StartsOn != "2026-09-21" || env.Data.EndsOn == nil || *env.Data.EndsOn != "2026-09-30" {
		t.Fatalf("respuesta: %+v", env.Data)
	}
	if repo.saved == nil || !strings.Contains(repo.saved.ScriptData, `currentdate :value "ge" "date" "2026-09-21"`) {
		t.Fatalf("se guardo sin script: %+v", repo.saved)
	}
	if strings.Contains(rec.Body.String(), "script_data") || strings.Contains(rec.Body.String(), "require") {
		t.Fatalf("el script no debe salir por la API: %s", rec.Body)
	}
}

func TestRespuestaAutomaticaRechazaEntradaInvalidaConUn422(t *testing.T) {
	h, repo, _, _ := vacationServer(t)
	casos := map[string]string{
		"activa sin mensaje":   `{"enabled":true}`,
		"fecha mal escrita":    `{"enabled":true,"message":"x","starts_on":"21/09/2026"}`,
		"fin antes del inicio": `{"enabled":true,"message":"x","starts_on":"2026-09-30","ends_on":"2026-09-21"}`,
		"intervalo fuera":      `{"enabled":true,"message":"x","interval_days":31}`,
		"asunto con salto":     `{"enabled":true,"message":"x","subject":"a\nBcc: x@y.z"}`,
	}
	for nombre, body := range casos {
		rec := doVacation(h, http.MethodPut, "/internal/mail-directory/vacation?username=ana@acme.test", body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", nombre, rec.Code, rec.Body)
		}
	}
	if repo.saved != nil {
		t.Fatal("una entrada invalida no debe guardarse")
	}
}

func TestRespuestaAutomaticaRechazaCuerposYNombresMalos(t *testing.T) {
	h, repo, _, _ := vacationServer(t)
	if rec := doVacation(h, http.MethodPut, "/internal/mail-directory/vacation?username=ana@acme.test", `{"enabled":true,"message":"x","script_data":"discard;"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("un campo desconocido (el usuario no puede mandar el script): %d %s", rec.Code, rec.Body)
	}
	if rec := doVacation(h, http.MethodPut, "/internal/mail-directory/vacation?username=ana@acme.test", `no es json`); rec.Code != http.StatusBadRequest {
		t.Errorf("JSON roto: %d", rec.Code)
	}
	huge := `{"enabled":true,"message":"` + strings.Repeat("a", 1<<20) + `"}`
	if rec := doVacation(h, http.MethodPut, "/internal/mail-directory/vacation?username=ana@acme.test", huge); rec.Code != http.StatusBadRequest {
		t.Errorf("cuerpo enorme: %d", rec.Code)
	}
	for _, malo := range []string{"", "sin-arroba", "%40x.test"} {
		if rec := doVacation(h, http.MethodGet, "/internal/mail-directory/vacation?username="+malo, ""); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("nombre %q: %d", malo, rec.Code)
		}
	}
	if rec := doVacation(h, http.MethodGet, "/internal/mail-directory/vacation?username=nadie@acme.test", ""); rec.Code != http.StatusNotFound {
		t.Errorf("buzon inexistente: %d", rec.Code)
	}
	if repo.saved != nil {
		t.Fatal("nada de esto debe guardarse")
	}
}

// Las rutas de administracion cuelgan de los permisos de sieve del modulo de buzones: sin sesion con
// permiso no llegan al caso de uso.
func TestRespuestaAutomaticaDeAdministracionExigePermiso(t *testing.T) {
	h, repo, _, m := vacationServer(t)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		rec := doVacation(h, method, "/api/v1/mailboxes/"+m.ID.String()+"/vacation", `{"enabled":true,"message":"x"}`)
		if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
			t.Errorf("%s sin permiso: %d %s", method, rec.Code, rec.Body)
		}
	}
	if repo.saved != nil {
		t.Fatal("sin permiso no se guarda")
	}
}
