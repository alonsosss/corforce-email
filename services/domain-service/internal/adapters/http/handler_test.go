package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
)

// Un nombre que no puede darse de alta es un error de validacion con el mensaje del dominio, que la
// web muestra tal cual.
func TestWriteErrorNombreNoAdmitido(t *testing.T) {
	for _, target := range []error{domain.ErrInvalidDomainName, domain.ErrPlatformDomain, domain.ErrPublicSuffixDomain} {
		rec := httptest.NewRecorder()
		writeError(rec, fmt.Errorf("alta: %w", target))
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%v: cuerpo %q: %v", target, rec.Body.String(), err)
		}
		if rec.Code != http.StatusUnprocessableEntity || body.Error.Code != "VALIDATION_ERROR" {
			t.Errorf("%v: %d %s", target, rec.Code, body.Error.Code)
		}
		if body.Error.Message == "" {
			t.Errorf("%v: sin mensaje", target)
		}
	}
}
