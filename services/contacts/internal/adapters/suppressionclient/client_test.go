package suppressionclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

func TestActiveCausesContract(t *testing.T) {
	tenant := uuid.New()
	body := ""
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req map[string][]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.Method != http.MethodPost || r.URL.Path != "/internal/suppression/check" ||
			r.Header.Get("X-Tenant-ID") != tenant.String() || r.Header.Get("X-Gateway-Token") != "tok" ||
			!reflect.DeepEqual(req["emails"], []string{"ana@example.com"}) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := New(srv.URL+"/", "tok")

	rebote := time.Date(2026, 9, 10, 8, 30, 0, 123456000, time.UTC)
	manual := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		body string
		want []domain.ActiveCause
	}{
		{`{"data":{"suppressed":[]}}`, []domain.ActiveCause{}},
		// Lo que devuelve el suppression actual: causes con la hora de alta de cada causa.
		{`{"data":{"suppressed":[{"email":"ana@example.com","reason":"hard_bounce","reasons":["hard_bounce","manual"],` +
			`"causes":[{"reason":"hard_bounce","created_at":"2026-09-10T08:30:00.123456Z"},{"reason":"manual","created_at":"2026-09-01T00:00:00Z"}]}]}}`,
			[]domain.ActiveCause{{Cause: domain.CauseHardBounce, RegisteredAt: rebote}, {Cause: domain.CauseManual, RegisteredAt: manual}}},
		// Una replica sin causes: las causas llegan sin hora.
		{`{"data":{"suppressed":[{"email":"ana@example.com","reason":"hard_bounce","reasons":["hard_bounce","manual"]}]}}`,
			[]domain.ActiveCause{{Cause: domain.CauseHardBounce}, {Cause: domain.CauseManual}}},
		// Una replica anterior sin reasons: la direccion sigue suprimida por su causa.
		{`{"data":{"suppressed":[{"email":"ana@example.com","reason":"complaint"}]}}`,
			[]domain.ActiveCause{{Cause: domain.CauseComplaint}}},
	}
	for _, tc := range cases {
		body = tc.body
		got, err := c.ActiveCauses(context.Background(), tenant, "ana@example.com")
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: %v %v", tc.body, got, err)
		}
	}

	for _, bad := range []string{
		`{"data":{"suppressed":[{"email":"ana@example.com","reasons":[]}]}}`,
		`{"data":{"suppressed":[{"email":"otra@example.com","reason":"manual","reasons":["manual"]}]}}`,
		`{"data":{"suppressed":[{"email":"ana@example.com","reason":"manual"},{"email":"ana@example.com","reason":"invalid"}]}}`,
		// causes que no nombra exactamente las causas de reasons.
		`{"data":{"suppressed":[{"email":"ana@example.com","reason":"unsubscribe","reasons":["unsubscribe","manual"],"causes":[{"reason":"unsubscribe","created_at":"2026-09-10T08:30:00Z"}]}]}}`,
		`{"data":{"suppressed":[{"email":"ana@example.com","reason":"unsubscribe","reasons":["unsubscribe"],"causes":[{"reason":"manual","created_at":"2026-09-10T08:30:00Z"}]}]}}`,
		`{"data":{"suppressed":[{"email":"ana@example.com","reason":"unsubscribe","reasons":["unsubscribe"],"causes":[]}]}}`,
		`{"data":{"suppressed":[{"email":"ana@example.com","reason":"unsubscribe","reasons":["unsubscribe"],"causes":[{"reason":"unsubscribe","created_at":"ayer"}]}]}}`,
		`no es json`,
	} {
		body = bad
		if _, err := c.ActiveCauses(context.Background(), tenant, "ana@example.com"); err == nil {
			t.Fatalf("%s: una respuesta incoherente nunca se lee como libre", bad)
		}
	}
}

// La consulta en bloque del barrido: tandas por debajo del tope de suppression, un mapa
// solo con las direcciones suprimidas y un error ante una respuesta que no corresponde a
// lo que se pidio.
func TestActiveCausesOfEnBloque(t *testing.T) {
	tenant := uuid.New()
	var batches [][]string
	extra := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string][]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.Method != http.MethodPost || r.URL.Path != checkPath ||
			r.Header.Get("X-Tenant-ID") != tenant.String() || r.Header.Get("X-Gateway-Token") != "tok" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		batches = append(batches, req["emails"])
		var items []string
		for _, e := range req["emails"] {
			if strings.HasPrefix(e, "excluida") {
				items = append(items, `{"email":"`+e+`","reason":"manual","reasons":["manual"],"causes":[{"reason":"manual","created_at":"2026-09-01T00:00:00Z"}]}`)
			}
		}
		if extra != "" {
			items = append(items, extra)
		}
		_, _ = fmt.Fprintf(w, `{"data":{"suppressed":[%s]}}`, strings.Join(items, ","))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	ctx := context.Background()

	emails := make([]string, checkBatch+2)
	excluded := 0
	for i := range emails {
		if i%2 == 0 {
			emails[i] = fmt.Sprintf("excluida%d@example.com", i)
			excluded++
		} else {
			emails[i] = fmt.Sprintf("libre%d@example.com", i)
		}
	}
	got, err := c.ActiveCausesOf(ctx, tenant, emails)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 || len(batches[0]) != checkBatch || len(batches[1]) != 2 {
		t.Fatalf("tandas de %d: %d consultas", checkBatch, len(batches))
	}
	if len(got) != excluded {
		t.Fatalf("solo las suprimidas: %d de %d", len(got), excluded)
	}
	want := []domain.ActiveCause{{Cause: domain.CauseManual, RegisteredAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}}
	if !reflect.DeepEqual(got["excluida0@example.com"], want) {
		t.Fatalf("causas con su hora: %v", got["excluida0@example.com"])
	}
	if _, ok := got["libre1@example.com"]; ok {
		t.Fatal("una direccion libre no aparece")
	}

	for _, bad := range []string{
		`{"email":"otra@example.com","reason":"manual","reasons":["manual"]}`,
		`{"email":"excluida0@example.com","reason":"manual","reasons":["manual"]}`,
		`{"email":"excluida-sin-causa@example.com"}`,
	} {
		extra = bad
		if _, err := c.ActiveCausesOf(ctx, tenant, []string{"excluida0@example.com", "excluida-sin-causa@example.com"}); err == nil {
			t.Fatalf("%s: una respuesta que no corresponde a lo pedido es un error", bad)
		}
	}

	extra, batches = "", nil
	if got, err := c.ActiveCausesOf(ctx, tenant, nil); err != nil || len(got) != 0 || len(batches) != 0 {
		t.Fatalf("sin direcciones no hay consulta: %v %v %d", got, err, len(batches))
	}
}

func TestActiveCausesSinReintentos(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "tok").ActiveCauses(context.Background(), uuid.New(), "ana@example.com"); err == nil {
		t.Fatal("un 503 es un error")
	}
	if requests != 1 {
		t.Fatalf("un solo intento: %d", requests)
	}
	if _, err := New("", "tok").ActiveCauses(context.Background(), uuid.New(), "ana@example.com"); err == nil {
		t.Fatal("sin SUPPRESSION_URL no hay consulta")
	}
}
