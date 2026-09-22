package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
)

// Los choques con el estado que la web distingue llevan su propio codigo: una empresa dada de baja y una
// direccion cuyo maildir anterior sigue en Dovecot (la web ofrece reintentar en unos minutos).
func TestWriteErrorDistingueLosConflictosConCodigo(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{domain.ErrTenantRetired, codeTenantRetired},
		{domain.ErrAddressRecentlyDeleted, codeAddressRecentlyDeleted},
		{domain.ErrAddressTaken, "CONFLICT"},
	} {
		rec := httptest.NewRecorder()
		writeError(rec, tc.err)
		if rec.Code != http.StatusConflict {
			t.Errorf("%v: estado %d, se esperaba 409", tc.err, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"`+tc.code+`"`) {
			t.Errorf("%v: sin el codigo %s en %s", tc.err, tc.code, rec.Body.String())
		}
	}
}
