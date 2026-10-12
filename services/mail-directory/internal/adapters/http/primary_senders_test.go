package http

import (
	"context"
	"net/http"
	"sort"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
)

type httpPrimarySenders struct {
	rows []domain.PrimarySender
}

var _ ports.PrimarySenderRepository = (*httpPrimarySenders)(nil)

func (f *httpPrimarySenders) ListByUsername(_ context.Context, tenantID uuid.UUID, username string) ([]domain.PrimarySender, error) {
	out := []domain.PrimarySender{}
	for _, s := range f.rows {
		if s.TenantID == tenantID && s.Username == username {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out, nil
}

func (f *httpPrimarySenders) Count(ctx context.Context, tenantID uuid.UUID, username string) (int, error) {
	l, _ := f.ListByUsername(ctx, tenantID, username)
	return len(l), nil
}

func (f *httpPrimarySenders) Add(_ context.Context, s *domain.PrimarySender) error {
	f.rows = append(f.rows, *s)
	return nil
}

func (f *httpPrimarySenders) Remove(_ context.Context, tenantID uuid.UUID, username, address string) error {
	kept := f.rows[:0]
	for _, s := range f.rows {
		if !(s.TenantID == tenantID && s.Username == username && s.Address == address) {
			kept = append(kept, s)
		}
	}
	f.rows = kept
	return nil
}

func (f *httpPrimarySenders) DeleteByUsername(context.Context, uuid.UUID, string) error { return nil }

func TestRemitentesFijadosInternos(t *testing.T) {
	e := remindersServer(t)
	base := "/internal/mail-directory/primary-senders?username=ana@acme.test"

	rec := e.do(http.MethodPost, base, `{"address":"Soporte@Proveedor.example"}`)
	var item primarySenderItem
	decodeEnvelope(t, rec, &item)
	if rec.Code != http.StatusOK || item.Address != "soporte@proveedor.example" {
		t.Fatalf("fijar: %d %s", rec.Code, rec.Body)
	}
	expectField(t, e.do(http.MethodPost, base, `{"address":"no es una direccion"}`), "address")

	rec = e.do(http.MethodGet, base, "")
	var list primarySendersResponse
	decodeEnvelope(t, rec, &list)
	if rec.Code != http.StatusOK || len(list.Items) != 1 || list.MaxItems != domain.MaxPrimarySenders {
		t.Fatalf("listar: %d %s", rec.Code, rec.Body)
	}

	if rec := e.do(http.MethodDelete, base+"&address=SOPORTE@proveedor.example", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("quitar: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(http.MethodDelete, base+"&address=soporte@proveedor.example", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("quitar dos veces es idempotente: %d", rec.Code)
	}
	expectField(t, e.do(http.MethodDelete, base, ""), "address")
}
