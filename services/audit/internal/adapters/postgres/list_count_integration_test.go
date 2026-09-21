//go:build integration

// Total del listado de la bitacora: exacto bajo db.PageCountCap y acotado por encima, y lo que
// cuesta en una empresa grande frente al count(*) exacto de antes. Para medir de verdad:
//
//	AUDIT_VOLUME_ROWS=100000 go test -tags integration -run TestListadoRecuento -v ./services/audit/internal/adapters/postgres
package postgres

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/alonsosss/corforce-email/services/audit/internal/ports"
	"github.com/google/uuid"
)

// seedRows inserta n filas de una empresa y un modulo sin pasar por la cadena: el listado no la
// lee.
func (e *env) seedRows(t *testing.T, tenant uuid.UUID, n int, module string) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(), `
INSERT INTO audit.audit_logs (tenant_id, user_id, action, module, resource, created_at)
SELECT $1, $2, 'listado.' || g, $3, 'recurso', now() - (g || ' seconds')::interval
  FROM generate_series(1, $4) g`, tenant, e.user, module, n); err != nil {
		t.Fatal(err)
	}
	if _, err := e.admin.Exec(context.Background(), `ANALYZE audit.audit_logs`); err != nil {
		t.Fatal(err)
	}
}

func TestListadoRecuentoExactoBajoElTopeYAcotadoSobreEl(t *testing.T) {
	e := setup(t)
	small := uuid.New()
	e.seedRows(t, small, 250, "identity")
	e.seedRows(t, e.tenant, int(db.PageCountCap)+500, "billing")
	e.seedRows(t, e.tenant, 30, "identity")

	for _, tc := range []struct {
		nombre string
		query  domain.AuditQuery
		want   ports.Total
	}{
		{"empresa pequena", domain.AuditQuery{TenantID: small}, ports.Total{Value: 250}},
		{"empresa grande sin filtro", domain.AuditQuery{TenantID: e.tenant}, ports.Total{Value: db.PageCountCap, Capped: true}},
		{"empresa grande, filtro que deja 30", domain.AuditQuery{TenantID: e.tenant, Module: ptr("identity")}, ports.Total{Value: 30}},
		{"empresa sin filas", domain.AuditQuery{TenantID: uuid.New()}, ports.Total{}},
	} {
		got, err := e.logs.Count(e.ctx, tc.query)
		if err != nil || got != tc.want {
			t.Fatalf("%s: Count = %+v, %v; se esperaba %+v", tc.nombre, got, err, tc.want)
		}
	}

	rows, err := e.logs.List(e.ctx, domain.AuditQuery{TenantID: e.tenant}, 3, 50)
	if err != nil || len(rows) != 50 {
		t.Fatalf("la pagina 3 del listado grande: %d filas, %v", len(rows), err)
	}
}

func TestListadoRecuentoCosto(t *testing.T) {
	e := setup(t)
	n := volumeRows(t)
	e.seedRows(t, e.tenant, n, "identity")
	q := domain.AuditQuery{TenantID: e.tenant}
	const rounds = 15

	mide := func(fn func()) time.Duration {
		fn()
		muestras := make([]time.Duration, rounds)
		for i := range muestras {
			s := time.Now()
			fn()
			muestras[i] = time.Since(s)
		}
		sort.Slice(muestras, func(i, j int) bool { return muestras[i] < muestras[j] })
		return muestras[rounds/2]
	}

	exacto := mide(func() {
		var total int64
		if err := e.svc.QueryRow(context.Background(), `SELECT COUNT(*) FROM audit.audit_logs WHERE tenant_id=$1`, e.tenant).Scan(&total); err != nil || total != int64(n) {
			t.Fatalf("recuento exacto: %d, %v", total, err)
		}
	})
	acotado := mide(func() {
		if _, err := e.logs.Count(e.ctx, q); err != nil {
			t.Fatal(err)
		}
	})
	pagina := mide(func() {
		if _, err := e.logs.List(e.ctx, q, 1, 50); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("con %d filas: count(*) exacto %s, recuento acotado a %d %s, pagina de 50 %s (medianas de %d)",
		n, exacto.Round(10*time.Microsecond), db.PageCountCap, acotado.Round(10*time.Microsecond), pagina.Round(10*time.Microsecond), rounds)
	if int64(n) > db.PageCountCap*3 && acotado >= exacto {
		t.Fatalf("el recuento acotado (%s) no es mas barato que el exacto (%s) con %d filas", acotado, exacto, n)
	}
}
