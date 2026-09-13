package suppressionclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

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

	cases := []struct {
		body string
		want []domain.SuppressionCause
	}{
		{`{"data":{"suppressed":[]}}`, []domain.SuppressionCause{}},
		{`{"data":{"suppressed":[{"email":"ana@example.com","reason":"hard_bounce","reasons":["hard_bounce","manual"]}]}}`,
			[]domain.SuppressionCause{domain.CauseHardBounce, domain.CauseManual}},
		// Una replica anterior sin reasons: la direccion sigue suprimida por su causa.
		{`{"data":{"suppressed":[{"email":"ana@example.com","reason":"complaint"}]}}`,
			[]domain.SuppressionCause{domain.CauseComplaint}},
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
		`no es json`,
	} {
		body = bad
		if _, err := c.ActiveCauses(context.Background(), tenant, "ana@example.com"); err == nil {
			t.Fatalf("%s: una respuesta incoherente nunca se lee como libre", bad)
		}
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
