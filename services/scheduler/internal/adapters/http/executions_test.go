package http

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/app"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// runningRepo sirve las ejecuciones activas como el SQL, con el desplazamiento del dominio,
// y anota lo que pidio el caso de uso.
type runningRepo struct {
	ports.JobExecutionRepository
	execs         []*domain.JobExecution
	tenant        uuid.UUID
	page, perPage int
}

func (r *runningRepo) ListRunning(_ context.Context, tenantID uuid.UUID, page, perPage int) ([]*domain.JobExecution, int64, error) {
	r.tenant, r.page, r.perPage = tenantID, page, perPage
	total := int64(len(r.execs))
	offset := domain.PageOffset(page, perPage)
	if offset >= total {
		return nil, total, nil
	}
	return r.execs[offset:min(int(offset)+perPage, len(r.execs))], total, nil
}

type rawPage struct {
	Data []json.RawMessage `json:"data"`
	Meta json.RawMessage   `json:"meta"`
}

func listRunning(t *testing.T, srv http.Handler, query, tenant string) (rawPage, map[string]json.RawMessage) {
	t.Helper()
	rec := request(t, srv, http.MethodGet, "/api/v1/scheduler/executions"+query, "", tenant)
	var env rawPage
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &env) != nil {
		t.Fatalf("GET /executions%s: %d %s", query, rec.Code, rec.Body.String())
	}
	meta, _ := keysOf(t, env.Meta)
	return env, meta
}

func TestEjecucionesActivasPaginadasComoLosTrabajos(t *testing.T) {
	me := uuid.New()
	repo := &runningRepo{}
	for i := 0; i < 3; i++ {
		repo.execs = append(repo.execs, &domain.JobExecution{
			ID: uuid.New(), JobID: uuid.New(), TenantID: &me, Status: domain.StatusRunning, CreatedAt: clockNow,
		})
	}
	uc := app.NewSchedulerUseCase(app.SchedulerDeps{Executions: repo, Logger: zap.NewNop()})
	srv := chi.NewRouter()
	srv.Use(middleware.InjectFromGateway)
	srv.Mount("/", NewHandler(Deps{UC: uc, Perms: authz.NewChecker(unreachable, "")}).Routes())

	env, meta := listRunning(t, srv, "?page=2&per_page=1&tenant_id="+uuid.NewString(), me.String())
	if _, keys := keysOf(t, env.Meta); keys != "page,per_page,total,total_pages" {
		t.Fatalf("claves de la meta: %s", keys)
	}
	for key, want := range map[string]string{"page": "2", "per_page": "1", "total": "3", "total_pages": "3"} {
		if string(meta[key]) != want {
			t.Errorf("meta.%s: %s, se esperaba %s", key, meta[key], want)
		}
	}
	if len(env.Data) != 1 || repo.tenant != me || repo.page != 2 || repo.perPage != 1 {
		t.Fatalf("pagina 2 de 1 de la empresa del token: %d filas, pedido %s %d/%d", len(env.Data), repo.tenant, repo.page, repo.perPage)
	}
	obj, keys := keysOf(t, env.Data[0])
	if keys != "completed_at,created_at,deadline_at,duration_ms,error_message,failure_reason,id,job_id,next_attempt_at,result,retry_count,retry_of,started_at,status,tenant_id" ||
		string(obj["id"]) != `"`+repo.execs[1].ID.String()+`"` {
		t.Fatalf("una fila es una ejecucion completa, la segunda: %s %s", keys, obj["id"])
	}

	_, meta = listRunning(t, srv, "?per_page=500&page=-3", me.String())
	if repo.perPage != maxPerPage || repo.page != 1 || string(meta["per_page"]) != strconv.Itoa(maxPerPage) {
		t.Fatalf("per_page de mas se recorta al maximo: %d/%d, meta %v", repo.page, repo.perPage, meta)
	}

	for _, page := range []string{strconv.Itoa(math.MaxInt), "99999999999999999999999"} {
		env, meta = listRunning(t, srv, "?page="+page, me.String())
		if len(env.Data) != 0 || string(meta["total"]) != "3" {
			t.Fatalf("pagina %s: %d filas, meta %v", page, len(env.Data), meta)
		}
	}

	repo.execs = nil
	env, meta = listRunning(t, srv, "", me.String())
	if string(mustJSON(t, env.Data)) != "[]" || string(meta["page"]) != "1" || string(meta["per_page"]) != strconv.Itoa(defaultPerPage) {
		t.Fatalf("sin activas: %s, meta %v", mustJSON(t, env.Data), meta)
	}
}
