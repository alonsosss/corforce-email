package outbox

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

type capture struct {
	subject string
	payload []byte
}

func (c *capture) Exec(_ context.Context, _ string, args ...interface{}) (pgconn.CommandTag, error) {
	c.subject = args[1].(string)
	c.payload = args[3].([]byte)
	return pgconn.CommandTag{}, nil
}

type envelope struct {
	Type     string                     `json:"type"`
	Source   string                     `json:"source"`
	TenantID string                     `json:"tenant_id"`
	Data     map[string]json.RawMessage `json:"data"`
}

func decode(t *testing.T, c *capture) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(c.payload, &env); err != nil {
		t.Fatalf("evento ilegible: %s", c.payload)
	}
	return env
}

func keys(m map[string]json.RawMessage) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// Un trabajo de plataforma lleva tenant_id null en data, pero el sobre lleva la empresa de
// la base: es la que el ejecutor devuelve en X-Tenant-ID al cerrar.
func TestJobStartedDeUnTrabajoDePlataforma(t *testing.T) {
	base := uuid.New()
	ctx := middleware.WithTenantID(context.Background(), base.String())
	payload := `{"days": 30}`
	job := &domain.JobDefinition{ID: uuid.New(), Handler: "ops.cleanup", Payload: &payload}
	exec := &domain.JobExecution{ID: uuid.New(), RetryCount: 1}

	c := &capture{}
	if err := NewPublisher(c).JobStarted(ctx, job, exec, 300); err != nil {
		t.Fatal(err)
	}
	env := decode(t, c)
	if c.subject != "scheduler.job.started" || env.Type != c.subject || env.Source != source || env.TenantID != base.String() {
		t.Fatalf("sobre: subject=%s %+v", c.subject, env)
	}
	if got := keys(env.Data); got != "attempt,execution_id,handler,job_id,payload,tenant_id,timeout_seconds" {
		t.Fatalf("claves: %s", got)
	}
	if string(env.Data["tenant_id"]) != "null" || string(env.Data["attempt"]) != "2" ||
		string(env.Data["timeout_seconds"]) != "300" || string(env.Data["handler"]) != `"ops.cleanup"` {
		t.Fatalf("data: %v", env.Data)
	}
	var p map[string]int
	if err := json.Unmarshal(env.Data["payload"], &p); err != nil || p["days"] != 30 {
		t.Fatalf("el payload viaja como objeto JSON: %s", env.Data["payload"])
	}
}

func TestJobFailedConYSinReintento(t *testing.T) {
	tenant := uuid.New()
	ctx := middleware.WithTenantID(context.Background(), tenant.String())
	job := &domain.JobDefinition{ID: uuid.New(), TenantID: &tenant, Handler: "reports.daily"}
	reason, msg := domain.FailureTimeout, "sin cierre"
	exec := &domain.JobExecution{ID: uuid.New(), FailureReason: &reason, ErrorMessage: &msg}
	at := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	retry := &domain.JobExecution{ID: uuid.New(), NextAttemptAt: &at}

	c := &capture{}
	if err := NewPublisher(c).JobFailed(ctx, job, exec, retry); err != nil {
		t.Fatal(err)
	}
	env := decode(t, c)
	if got := keys(env.Data); got != "attempt,error,execution_id,handler,job_id,reason,retry_at,retry_execution_id,tenant_id" {
		t.Fatalf("claves: %s", got)
	}
	if string(env.Data["retry_execution_id"]) != `"`+retry.ID.String()+`"` || string(env.Data["retry_at"]) != `"2026-09-13T12:00:00Z"` ||
		string(env.Data["reason"]) != `"timeout"` || string(env.Data["tenant_id"]) != `"`+tenant.String()+`"` {
		t.Fatalf("data: %v", env.Data)
	}

	if err := NewPublisher(c).JobFailed(ctx, job, exec, nil); err != nil {
		t.Fatal(err)
	}
	env = decode(t, c)
	if string(env.Data["retry_execution_id"]) != "null" || string(env.Data["retry_at"]) != "null" {
		t.Fatalf("sin reintento: %v", env.Data)
	}
}

func TestJobCompleted(t *testing.T) {
	c := &capture{}
	d := int64(1500)
	job := &domain.JobDefinition{ID: uuid.New(), Handler: "reports.daily"}
	exec := &domain.JobExecution{ID: uuid.New(), Duration: &d}
	if err := NewPublisher(c).JobCompleted(context.Background(), job, exec); err != nil {
		t.Fatal(err)
	}
	env := decode(t, c)
	if c.subject != "scheduler.job.completed" || keys(env.Data) != "attempt,duration_ms,execution_id,handler,job_id,tenant_id" ||
		string(env.Data["duration_ms"]) != "1500" {
		t.Fatalf("completed: %s %v", c.subject, env.Data)
	}
}
