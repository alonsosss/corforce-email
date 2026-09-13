//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	outboxadapter "github.com/alonsosss/corforce-email/services/reputation/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/reputation/internal/app"
	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

type openRate struct{}

func (openRate) Reserve(_ context.Context, _ uuid.UUID, _ domain.Class, _ time.Time, count int64, _ domain.Limits) (domain.RateOutcome, error) {
	return domain.RateOutcome{Allowed: true, HourUsed: count, DayUsed: count}, nil
}

func (openRate) Usage(context.Context, uuid.UUID, domain.Class, time.Time) (int64, int64, error) {
	return 0, 0, nil
}

type openBilling struct{}

func (openBilling) Check(_ context.Context, _ uuid.UUID, _ domain.Class, quantity int64) (domain.Entitlement, error) {
	return domain.Entitlement{Allowed: true, Requested: quantity}, nil
}

// sameBase apunta cualquier empresa a la base de prueba.
type sameBase struct{}

func (sameBase) Scope(ctx context.Context, tenantID uuid.UUID) (context.Context, error) {
	return ctx, nil
}

func (sameBase) ForEachActive(context.Context, time.Duration, func(context.Context, uuid.UUID)) error {
	return nil
}

type noMetrics struct{}

func (noMetrics) Authorize(domain.Class, string)          {}
func (noMetrics) StateChanged(domain.Class, domain.State) {}
func (noMetrics) Degraded(string)                         {}

// TestFlujoCompletoSobrePostgres ejercita el caso de uso con los repositorios reales y la
// outbox: contar, deduplicar, reevaluar bajo bloqueo, historial y evento en la misma
// transaccion, autorizacion y suspension manual.
func TestFlujoCompletoSobrePostgres(t *testing.T) {
	pool, ctx := testDB(t)
	cp := &db.ContextPool{}
	uc := app.New(app.Deps{
		Stats: NewStatsRepository(cp), States: NewStateRepository(cp), Limits: NewLimitRepository(cp),
		Tx: cp, Events: outboxadapter.NewPublisher(cp), Rate: openRate{}, Billing: openBilling{},
		Tenants: sameBase{}, Metrics: noMetrics{},
		Policy: domain.Policy{
			Thresholds: domain.Thresholds{
				BounceWarn: decimal.RequireFromString("0.02"), BounceBlock: decimal.RequireFromString("0.04"),
				ComplaintWarn: decimal.RequireFromString("0.0005"), ComplaintBlock: decimal.RequireFromString("0.0008"),
				MinVolume: 200,
			},
			WindowDays: 7,
			Defaults: map[domain.Class]domain.Limits{
				domain.ClassTransactional: {Hourly: 100, Daily: 1000},
				domain.ClassMarketing:     {Hourly: 100, Daily: 1000},
			},
		},
		Logger: zap.NewNop(),
	})
	tenant := uuid.New()

	for i := 0; i < 25; i++ {
		ev := app.DeliveryEvent{EventID: fmt.Sprintf("%s-s-%d", tenant, i), TenantID: tenant, Kind: app.KindSent,
			Class: domain.ClassMarketing, Recipients: 10}
		if _, err := uc.RecordDelivery(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 12; i++ {
		ev := app.DeliveryEvent{EventID: fmt.Sprintf("%s-b-%d", tenant, i), TenantID: tenant, Kind: app.KindBounced,
			Class: domain.ClassMarketing, BounceType: app.BounceTypePermanent}
		if _, err := uc.RecordDelivery(ctx, ev); err != nil {
			t.Fatal(err)
		}
		// La reentrega de cada rebote no suma.
		if res, err := uc.RecordDelivery(ctx, ev); err != nil || !res.Duplicate {
			t.Fatalf("reentrega: %+v %v", res, err)
		}
	}

	st, err := uc.Status(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	mk := st.Classes[1]
	if mk.Counts.Sent != 250 || mk.Counts.Bounced != 12 || mk.BounceRate.StringFixed(domain.RateScale) != "0.048000" {
		t.Fatalf("ventana: %+v tasa=%s", mk.Counts, mk.BounceRate)
	}
	if mk.Record.State != domain.StateRestricted || mk.Record.BounceRate.StringFixed(domain.RateScale) != "0.040000" {
		t.Fatalf("estado: %+v", mk.Record)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM reputation.state_history WHERE tenant_id = $1`, tenant).Scan(&n); err != nil || n != 2 {
		t.Fatalf("historial ok->warning->restricted: %d %v", n, err)
	}
	var payload []byte
	if err := pool.QueryRow(ctx,
		`SELECT payload FROM platform.event_outbox WHERE tenant_id = $1 AND subject = $2 ORDER BY created_at DESC LIMIT 1`,
		tenant, outboxadapter.SubjectStateChanged).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Type string                 `json:"type"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil {
		t.Fatal(err)
	}
	if evt.Type != outboxadapter.SubjectStateChanged || evt.Data["from"] != "warning" || evt.Data["to"] != "restricted" ||
		evt.Data["bounce_rate"] != "0.040000" || evt.Data["manual"] != false || evt.Data["class"] != "marketing" {
		t.Fatalf("evento: %+v", evt)
	}

	dec, err := uc.Authorize(ctx, app.AuthorizeInput{TenantID: tenant, Class: domain.ClassMarketing, Count: 1})
	if err != nil || dec.Allowed || dec.Reason != domain.ReasonReputationRestricted {
		t.Fatalf("autorizacion: %+v %v", dec, err)
	}

	admin := uuid.New()
	rec, err := uc.Suspend(ctx, tenant, domain.ClassMarketing, "revision de listas", admin)
	if err != nil || rec.State != domain.StateSuspended || !rec.Manual {
		t.Fatalf("suspension: %+v %v", rec, err)
	}
	if _, changed, err := uc.Reevaluate(ctx, tenant, domain.ClassMarketing); err != nil || changed {
		t.Fatalf("la evaluacion no pisa lo manual: changed=%v err=%v", changed, err)
	}
	var userID string
	if err := pool.QueryRow(ctx,
		`SELECT payload->>'user_id' FROM platform.event_outbox WHERE tenant_id = $1 AND subject = $2 AND payload->'data'->>'to' = 'suspended'`,
		tenant, outboxadapter.SubjectStateChanged).Scan(&userID); err != nil || userID != admin.String() {
		t.Fatalf("el evento de la suspension lleva al superadmin: %q %v", userID, err)
	}
}
