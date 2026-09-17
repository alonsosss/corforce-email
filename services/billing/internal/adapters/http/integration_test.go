//go:build integration

package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	outboxadapter "github.com/alonsosss/corforce-email/services/billing/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/billing/internal/adapters/postgres"
	"github.com/alonsosss/corforce-email/services/billing/internal/app"
	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testServer monta las rutas como main, sobre el registro de BILLING_TEST_DSN (una base
// desechable a la que aplica dos veces las migraciones del registro) y con BILLING_ENFORCE
// encendido.
func testServer(t *testing.T) (http.Handler, *app.UseCase) {
	t.Helper()
	dsn := integrationEnv(t, "BILLING_TEST_DSN")
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	applyRegistryMigrationsTwice(t, dsn)
	store := postgres.NewStore(pool)
	uc := app.New(app.Deps{
		Plans: postgres.NewPlanRepository(store), Subscriptions: postgres.NewSubscriptionRepository(store),
		Usage: postgres.NewUsageRepository(store), Ledger: postgres.NewLedger(store), Tx: store,
		Events: outboxadapter.NewPublisher(store), Config: app.Config{Enforce: true},
	})
	// Los roles del sistema pasan sin consultar a access-control: la URL no llega a usarse.
	h := NewHandler(uc, authz.NewChecker("http://127.0.0.1:9", ""))
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount("/api/v1/billing", h.PublicRoutes())
	r.Mount("/internal/billing", h.InternalRoutes())
	return r, uc
}

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

// applyRegistryMigrationsTwice recorre dos veces el registro completo en orden numerico: la
// segunda pasada demuestra que cada migracion tolera re-ejecutarse. Corre en una conexion
// propia que conserva hasta el final de la prueba el candado asesor que toman tambien
// access-control e identity (otro paquete que migre la misma base mientras esta siembra
// provoca bloqueos mutuos entre el DDL y los INSERT), sin quitarle conexiones al pool.
func applyRegistryMigrationsTwice(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join("..", "..", "..", "..", "..", "migrations", "registry")
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("migraciones del registro en %s: %v", dir, err)
	}
	order := func(path string) int {
		n, convErr := strconv.Atoi(strings.SplitN(filepath.Base(path), "_", 2)[0])
		if convErr != nil {
			t.Fatalf("migracion sin numero: %s", path)
		}
		return n
	}
	sort.Slice(files, func(i, j int) bool { return order(files[i]) < order(files[j]) })

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("conexion de migracion: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('registry-migrations-it'))`); err != nil {
		t.Fatalf("candado: %v", err)
	}
	for pass := 1; pass <= 2; pass++ {
		for _, f := range files {
			sql, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("leer %s: %v", f, err)
			}
			if _, err := conn.Exec(ctx, string(sql)); err != nil {
				t.Fatalf("pasada %d, %s: %v", pass, filepath.Base(f), err)
			}
		}
	}
}

type envelope struct {
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func call(t *testing.T, srv http.Handler, method, path, body string, tenant uuid.UUID, role string) (int, envelope) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", uuid.NewString())
	req.Header.Set("X-Tenant-ID", tenant.String())
	if role != "" {
		req.Header.Set("X-User-Roles", role)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("%s %s: cuerpo ilegible %q", method, path, rec.Body.String())
	}
	return rec.Code, env
}

// callInternal reproduce como llaman los servicios reales a una ruta interna (por
// ejemplo reputation/billingcli a /internal/billing/entitlements/check): solo
// X-Tenant-ID, nunca X-User-ID. RequireInternalCaller rechaza cualquier peticion con
// usuario, asi que reusar call() aqui (que siempre fija X-User-ID) daria un falso 403.
func callInternal(t *testing.T, srv http.Handler, method, path, body string, tenant uuid.UUID) (int, envelope) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", tenant.String())
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("%s %s: cuerpo ilegible %q", method, path, rec.Body.String())
	}
	return rec.Code, env
}

func planBody(code string) string {
	var limits []map[string]interface{}
	for _, r := range domain.Resources() {
		l := map[string]interface{}{"resource": string(r), "included": -1, "hard_limit": true}
		switch r {
		case domain.ResourceMailboxes:
			l["included"] = 1
		case domain.ResourceTransactionalMessages:
			l["included"], l["hard_limit"], l["overage_unit_price"] = 100, false, "0.0015"
		}
		limits = append(limits, l)
	}
	b, _ := json.Marshal(map[string]interface{}{
		"code": code, "name": "Plan HTTP", "currency": "USD", "base_price": "49.90",
		"billing_period": "monthly", "limits": limits,
	})
	return string(b)
}

func TestContratoHTTP(t *testing.T) {
	srv, uc := testServer(t)
	platform, tenant := uuid.New(), uuid.New()
	code := "http-" + uuid.NewString()[:8]
	super, admin := middleware.RoleSuperadmin, middleware.RoleTenantAdmin

	if status, env := call(t, srv, "POST", "/api/v1/billing/plans", planBody(code), tenant, admin); status != http.StatusForbidden || env.Error == nil {
		t.Fatalf("un tenant_admin no crea planes: %d", status)
	}
	if status, _ := call(t, srv, "POST", "/api/v1/billing/plans",
		`{"code":"num-price","name":"n","currency":"USD","base_price":49.9,"billing_period":"monthly","limits":[]}`, platform, super); status != http.StatusBadRequest {
		t.Fatalf("un importe como numero JSON se rechaza: %d", status)
	}

	status, env := call(t, srv, "POST", "/api/v1/billing/plans", planBody(code), platform, super)
	if status != http.StatusCreated {
		t.Fatalf("alta de plan: %d %+v", status, env.Error)
	}
	var plan struct {
		ID        string `json:"id"`
		BasePrice string `json:"base_price"`
		Limits    []struct {
			Resource         string  `json:"resource"`
			OverageUnitPrice *string `json:"overage_unit_price"`
		} `json:"limits"`
	}
	if err := json.Unmarshal(env.Data, &plan); err != nil || plan.BasePrice != "49.90" {
		t.Fatalf("base_price como texto con escala: %s %v", env.Data, err)
	}
	for _, l := range plan.Limits {
		if l.Resource == "transactional_messages" && (l.OverageUnitPrice == nil || *l.OverageUnitPrice != "0.001500") {
			t.Fatalf("overage_unit_price: %v", l.OverageUnitPrice)
		}
	}

	check := func(body string) entitlementDTO {
		t.Helper()
		status, env := callInternal(t, srv, "POST", "/internal/billing/entitlements/check", body, tenant)
		if status != http.StatusOK {
			t.Fatalf("entitlements/check: %d %+v", status, env.Error)
		}
		var ent entitlementDTO
		if err := json.Unmarshal(env.Data, &ent); err != nil {
			t.Fatal(err)
		}
		return ent
	}
	if ent := check(`{"resource":"mailboxes"}`); ent.Allowed || ent.Reason != "no_subscription" {
		t.Fatalf("con enforce y sin suscripcion se deniega: %+v", ent)
	}

	if status, env := call(t, srv, "PUT", "/api/v1/billing/subscriptions/"+tenant.String(), `{"plan_code":"`+code+`"}`, platform, super); status != http.StatusCreated {
		t.Fatalf("asignar plan: %d %+v", status, env.Error)
	}
	if status, _ := call(t, srv, "PUT", "/api/v1/billing/subscriptions/"+tenant.String(), `{"plan_code":"`+code+`"}`, platform, super); status != http.StatusOK {
		t.Fatalf("repetir la asignacion: %d", status)
	}
	if status, env := call(t, srv, "PATCH", "/api/v1/billing/plans/"+plan.ID, `{"base_price":"59.90"}`, platform, super); status != http.StatusConflict || env.Error.Code != "PLAN_IN_USE" {
		t.Fatalf("plan con suscripciones: %d %+v", status, env.Error)
	}

	if ent := check(`{"resource":"mailboxes","quantity":1}`); !ent.Allowed || ent.Limit == nil || *ent.Limit != 1 || *ent.Remaining != 1 {
		t.Fatalf("con margen: %+v", ent)
	}
	if _, err := uc.RecordUsage(context.Background(), domain.UsageChange{EventID: uuid.NewString(), Subject: "mail.mailbox.created",
		TenantID: tenant, Resource: domain.ResourceMailboxes, Delta: 1}); err != nil {
		t.Fatal(err)
	}
	if ent := check(`{"resource":"mailboxes"}`); ent.Allowed || ent.Reason != "limit_reached" || ent.Used != 1 || *ent.Remaining != 0 {
		t.Fatalf("en el tope: %+v", ent)
	}
	if status, _ := callInternal(t, srv, "POST", "/internal/billing/entitlements/check", `{"resource":"gigas"}`, tenant); status != http.StatusUnprocessableEntity {
		t.Fatalf("recurso desconocido: %d", status)
	}

	status, env = call(t, srv, "GET", "/api/v1/billing/usage", "", tenant, admin)
	if status != http.StatusOK {
		t.Fatalf("consumo: %d %+v", status, env.Error)
	}
	var usage usageDTO
	if err := json.Unmarshal(env.Data, &usage); err != nil {
		t.Fatal(err)
	}
	for _, l := range usage.Resources {
		switch l.Resource {
		case "mailboxes":
			if l.Used != 1 || l.Percent == nil || *l.Percent != "100.00" || l.Kind != "stock" {
				t.Fatalf("buzones: %+v", l)
			}
		case "transactional_messages":
			if l.Percent == nil || *l.Percent != "0.00" || l.Kind != "flow" {
				t.Fatalf("mensajes: %+v", l)
			}
		}
	}

	status, env = call(t, srv, "GET", "/api/v1/billing/subscription", "", tenant, admin)
	var sub subscriptionDTO
	if status != http.StatusOK || json.Unmarshal(env.Data, &sub) != nil || sub.Plan == nil || sub.Plan.BasePrice != "49.90" || sub.PlanCode != code {
		t.Fatalf("suscripcion de la empresa: %d %s", status, env.Data)
	}
	if status, _ := call(t, srv, "GET", "/api/v1/billing/usage/"+tenant.String(), "", platform, super); status != http.StatusOK {
		t.Fatalf("consumo visto por la plataforma: %d", status)
	}
	if status, env := call(t, srv, "GET", "/api/v1/billing/usage", "", uuid.New(), admin); status != http.StatusNotFound || env.Error.Code != "NO_SUBSCRIPTION" {
		t.Fatalf("empresa sin suscripcion: %d %+v", status, env.Error)
	}
	if status, _ := call(t, srv, "GET", "/api/v1/billing/subscriptions?status=bogus", "", platform, super); status != http.StatusUnprocessableEntity {
		t.Fatalf("filtro de estado invalido: %d", status)
	}
	if status, _ := call(t, srv, "GET", "/api/v1/billing/subscriptions?per_page=500", "", platform, admin); status != http.StatusForbidden {
		t.Fatalf("un tenant_admin no lista suscripciones de otras empresas: %d", status)
	}
}
