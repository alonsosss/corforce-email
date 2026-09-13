//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	supoutbox "github.com/alonsosss/corforce-email/services/suppression/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/suppression/internal/app"
	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// lockWait simula lo que una transaccion de alta puede esperar al bloqueo de la direccion
// entre que empieza y que escribe la fila.
const lockWait = 200 * time.Millisecond

// columnDefault devuelve la expresion DEFAULT de una columna de suppression.entries.
func columnDefault(t *testing.T, ctx context.Context, pool *pgxpool.Pool, column string) string {
	t.Helper()
	var expr string
	if err := pool.QueryRow(ctx, `SELECT pg_get_expr(d.adbin, d.adrelid)
		  FROM pg_attrdef d
		  JOIN pg_attribute a ON a.attrelid = d.adrelid AND a.attnum = d.adnum
		 WHERE d.adrelid = 'suppression.entries'::regclass AND a.attname = $1`, column).Scan(&expr); err != nil {
		t.Fatalf("DEFAULT de %s: %v", column, err)
	}
	return expr
}

// startAndWait devuelve la hora de inicio de la transaccion del contexto (now()) despues
// de esperar lockWait dentro de ella.
func startAndWait(ctx context.Context, cp *db.ContextPool) (time.Time, error) {
	var start time.Time
	if err := cp.QueryRow(ctx, `SELECT now()`).Scan(&start); err != nil {
		return time.Time{}, err
	}
	_, err := cp.Exec(ctx, `SELECT pg_sleep($1::float8)`, lockWait.Seconds())
	return start, err
}

func dbClock(t *testing.T, ctx context.Context, pool *pgxpool.Pool) time.Time {
	t.Helper()
	var now time.Time
	if err := pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now
}

// Tras las migraciones (aplicadas dos veces por testPool), las altas se fechan con la hora
// de la sentencia que escribe la fila, no con la del inicio de su transaccion: un alta que
// espero el bloqueo de la direccion no queda por delante de un consentimiento confirmado
// mientras tanto.
func TestAltaFechadaConElRelojDeLaSentencia(t *testing.T) {
	pool, ctx := testPool(t)
	for _, column := range []string{"created_at", "updated_at"} {
		if got := columnDefault(t, ctx, pool, column); got != "clock_timestamp()" {
			t.Fatalf("DEFAULT de %s: %q", column, got)
		}
	}

	cp := &db.ContextPool{}
	repo := NewRepository(cp)
	tenant := uuid.New()
	var (
		start  time.Time
		single = &domain.Entry{TenantID: tenant, Email: "ana@example.com", Reason: domain.ReasonUnsubscribe, Source: "transactional"}
		bulk   []domain.Entry
	)
	err := cp.Transact(ctx, func(ctx context.Context) error {
		var err error
		if start, err = startAndWait(ctx, cp); err != nil {
			return err
		}
		if err := repo.Insert(ctx, single); err != nil {
			return err
		}
		bulk, err = repo.InsertMissing(ctx, tenant, []string{"eva@example.com"}, domain.ReasonManual, "import", "", time.Now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bulk) != 1 {
		t.Fatalf("carga: %+v", bulk)
	}
	for _, e := range []domain.Entry{*single, bulk[0]} {
		if e.CreatedAt.Sub(start) < lockWait {
			t.Fatalf("%s fechada al inicio de la transaccion: inicio %s, created_at %s", e.Email, start, e.CreatedAt)
		}
		if e.UpdatedAt.Before(e.CreatedAt) {
			t.Fatalf("%s: updated_at %s anterior a created_at %s", e.Email, e.UpdatedAt, e.CreatedAt)
		}
	}
}

// Reregister guarda los datos nuevos en la misma fila y la vuelve a fechar con la hora de
// la sentencia; otra empresa no la alcanza.
func TestReregisterVuelveAFecharLaCausa(t *testing.T) {
	_, ctx := testPool(t)
	cp := &db.ContextPool{}
	repo := NewRepository(cp)
	tenant := uuid.New()

	e := &domain.Entry{TenantID: tenant, Email: "luis@example.com", Reason: domain.ReasonUnsubscribe, Source: "transactional", Detail: "primera"}
	if err := repo.Insert(ctx, e); err != nil {
		t.Fatal(err)
	}
	first := e.CreatedAt
	msg, campaign := uuid.New(), uuid.New()
	var start time.Time
	err := cp.Transact(ctx, func(ctx context.Context) error {
		var err error
		if start, err = startAndWait(ctx, cp); err != nil {
			return err
		}
		e.Source, e.Detail, e.MessageID, e.CampaignID = "campaign", "segunda", &msg, &campaign
		return repo.Reregister(ctx, e)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !e.CreatedAt.After(first) || e.CreatedAt.Sub(start) < lockWait {
		t.Fatalf("hora de alta: antes %s, inicio %s, ahora %s", first, start, e.CreatedAt)
	}
	stored, err := repo.GetByID(ctx, tenant, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.CreatedAt.Equal(e.CreatedAt) || stored.Source != "campaign" || stored.Detail != "segunda" ||
		stored.MessageID == nil || *stored.MessageID != msg || stored.CampaignID == nil || *stored.CampaignID != campaign {
		t.Fatalf("fila guardada: %+v", stored)
	}

	other := *e
	other.TenantID = uuid.New()
	if err := repo.Reregister(ctx, &other); !errors.Is(err, domain.ErrEntryNotFound) {
		t.Fatalf("otra empresa: se esperaba ErrEntryNotFound, hubo %v", err)
	}
	other = *e
	other.ID = uuid.New()
	if err := repo.Reregister(ctx, &other); !errors.Is(err, domain.ErrEntryNotFound) {
		t.Fatalf("id inexistente: se esperaba ErrEntryNotFound, hubo %v", err)
	}
}

// El hueco completo contra la base: baja, reconsentimiento (fechado por el mismo reloj) y
// una baja nueva antes de procesar contacts.contact.resubscribed. La baja nueva sale otra
// vez por la outbox con otro id, y ni la resuscripcion ni su reentrega la retiran; un
// consentimiento posterior si.
func TestBajaRepetidaYResuscripcionContraLaBase(t *testing.T) {
	pool, ctx := testPool(t)
	cp := &db.ContextPool{}
	uc := app.New(app.Deps{
		Entries: NewRepository(cp), Imports: NewImportRepository(cp), Tx: cp,
		Events: supoutbox.NewPublisher(cp), Logger: zap.NewNop(),
	})
	tenant := uuid.New()
	const email = "vuelve@example.com"
	unsubscribe := app.AddInput{Email: email, Reason: domain.ReasonUnsubscribe, Source: "transactional"}

	if _, added, err := uc.Add(ctx, tenant, unsubscribe); err != nil || !added {
		t.Fatalf("primera baja: added=%v err=%v", added, err)
	}
	consentedAt := dbClock(t, ctx, pool)
	if _, added, err := uc.Add(ctx, tenant, unsubscribe); err != nil || !added {
		t.Fatalf("baja tras reconsentir: added=%v err=%v", added, err)
	}

	outboxIDs := func(subject string) []string {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT id::text FROM platform.event_outbox WHERE tenant_id = $1 AND subject = $2`, tenant, subject)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return ids
	}
	if ids := outboxIDs(supoutbox.SubjectEntryAdded); len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("dos altas con ids distintos: %v", ids)
	}

	for i := 0; i < 2; i++ {
		if removed, err := uc.Resubscribe(ctx, tenant, email, &consentedAt); err != nil || removed {
			t.Fatalf("entrega %d de la resuscripcion: removed=%v err=%v", i+1, removed, err)
		}
	}
	got, err := uc.Check(ctx, tenant, []string{email})
	if err != nil || len(got) != 1 || len(got[0].Causes) != 1 || !got[0].Causes[0].CreatedAt.After(consentedAt) {
		t.Fatalf("la baja nueva sigue, posterior al consentimiento %s: %+v err=%v", consentedAt, got, err)
	}
	if ids := outboxIDs(supoutbox.SubjectEntryRemoved); len(ids) != 0 {
		t.Fatalf("ninguna retirada: %v", ids)
	}

	later := dbClock(t, ctx, pool)
	if removed, err := uc.Resubscribe(ctx, tenant, email, &later); err != nil || !removed {
		t.Fatalf("consentimiento posterior: removed=%v err=%v", removed, err)
	}
	if got, _ := uc.Check(ctx, tenant, []string{email}); len(got) != 0 {
		t.Fatalf("libre tras el consentimiento posterior: %+v", got)
	}
	if ids := outboxIDs(supoutbox.SubjectEntryRemoved); len(ids) != 1 {
		t.Fatalf("una retirada: %v", ids)
	}
}
