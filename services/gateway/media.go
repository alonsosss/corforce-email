package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/objectstore"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// mediaPresignTTL es la vida util de la URL prefirmada que se entrega al cliente.
// Es corta a proposito: la URL estable persistida apunta al gateway, no al bucket,
// por lo que la firma se renueva en cada peticion.
const mediaPresignTTL = 15 * time.Minute

// mediaHandler sirve UNICAMENTE los objetos del espacio public/ (imagenes de
// catalogo, medios de tienda, creatividades): activos no confidenciales,
// legibles por cualquiera con el enlace. Redirige a una URL prefirmada de corta
// vida sobre el bucket privado, preservando la lectura por <img>/descarga del
// navegador (que no envia Authorization) y el fetch de terceros (Meta sigue el 302).
//
// Los objetos private/ (legajo de RR.HH., adjuntos de chat, media de WhatsApp)
// NO se sirven aqui: cada servicio entrega una URL prefirmada en su propia
// respuesta, acotada por el tenant autenticado. Asi el gateway nunca firma una
// clave privada sin validar identidad, evitando el acceso cruzado entre tenants.
func mediaHandler(store *objectstore.Store, logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := chi.URLParam(r, "*")
		if !strings.HasPrefix(key, "public/") {
			response.Err(w, http.StatusNotFound, "NOT_FOUND", "not found")
			return
		}
		u, err := store.PresignGet(r.Context(), key, mediaPresignTTL)
		if err != nil {
			logger.Warn("media presign failed", zap.String("key", key), zap.Error(err))
			response.Err(w, http.StatusBadGateway, "MEDIA_UNAVAILABLE", "media unavailable")
			return
		}
		http.Redirect(w, r, u.String(), http.StatusFound)
	}
}
