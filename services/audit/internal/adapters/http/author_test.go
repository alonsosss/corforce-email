package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/google/uuid"
)

// Con sesion, un apunte solo se registra a nombre de quien la tiene: el rechazo ocurre
// antes de tocar el caso de uso (los tests montan las rutas sin el).
func TestNadieRegistraApuntesANombreDeOtro(t *testing.T) {
	srv := newServer(unreachable)
	otro := uuid.NewString()
	casos := []struct{ path, body string }{
		{base + "/logs", `{"user_id":"` + otro + `","action":"x","module":"m","resource":"r","severity":"info"}`},
		{base + "/logs/bulk", `{"logs":[{"user_id":"` + otro + `","action":"x","module":"m","resource":"r","severity":"info"}]}`},
	}
	for _, c := range casos {
		req := httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", uuid.NewString())
		req.Header.Set("X-Tenant-ID", uuid.NewString())
		req.Header.Set("X-User-Roles", middleware.RoleTenantAdmin)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s a nombre de otro usuario: %d, se esperaba 403", c.path, rec.Code)
		}
	}
}
