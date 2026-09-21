package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// El gateway firma una URL sobre el bucket privado sin sesion, asi que solo firma claves del espacio
// public/. Una clave que sale de el con segmentos ".." (o con barra invertida) no se firma nunca: el
// destino la interpretaria como un objeto de otro espacio.
func TestMediaNoFirmaClavesQueSalenDeSuEspacio(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/media/*", mediaHandler(nil, zap.NewNop()))
	for _, path := range []string{
		"/media/private/tenant/adjunto.pdf",
		"/media/publicx/logo.png",
		"/media/public/../private/tenant/adjunto.pdf",
		"/media/public/%2e%2e/private/tenant/adjunto.pdf",
		"/media/public/a/./b.png",
		"/media/public//b.png",
		"/media/public/a%5Cb.png",
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, se esperaba 404 sin firmar nada", path, rec.Code)
		}
	}
}
