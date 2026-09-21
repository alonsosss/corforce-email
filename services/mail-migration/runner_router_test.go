package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	handler "github.com/alonsosss/corforce-email/services/mail-migration/internal/adapters/http"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/app"
	"go.uber.org/zap"
)

// El listener del ejecutor no esta detras del gateway: nadie pone X-Real-IP ni X-Forwarded-For, asi
// que lo que traiga la peticion lo escribe quien la envia. Si el limitador se fiara de esas
// cabeceras, cualquier miembro de la red del ejecutor esquivaria el limite cambiandolas, y podria
// agotar el cupo de la IP del ejecutor real haciendose pasar por ella.
func TestLimitadorDelEjecutorNoSeFiaDeCabecerasDeIP(t *testing.T) {
	uc := app.New(app.Deps{})
	router := runnerRouter(handler.NewRunnerHandler(uc, strings.Repeat("k", 40), zap.NewNop()), zap.NewNop())

	limited := 0
	for i := 0; i < runnerRateLimit+100; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/claim", strings.NewReader(`{"runner_id":"r"}`))
		req.Header.Set("Authorization", "Bearer "+strings.Repeat("k", 40))
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i%250))
		req.Header.Set("X-Real-IP", fmt.Sprintf("198.51.100.%d", i%250))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("cambiar X-Forwarded-For o X-Real-IP esquiva el limite del listener del ejecutor")
	}
}
