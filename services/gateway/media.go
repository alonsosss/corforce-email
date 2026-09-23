package main

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/objectstore"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// mediaMaxBytes es el mayor objeto que el gateway sirve. Las imagenes de plantillas entran con
// 5 MiB como mucho (POST /assets de templates); el margen cubre lo subido antes por otros caminos
// sin dejar que una clave publica sirva de canal para ficheros grandes.
const mediaMaxBytes = 10 << 20

// mediaCacheControl: la clave de un objeto public/ lleva el sha256 de su contenido, asi que un
// mismo URL nunca cambia de bytes y el cliente (o el proxy de imagenes de un proveedor de correo)
// puede guardarlo para siempre.
const mediaCacheControl = "public, max-age=31536000, immutable"

// mediaContentTypes es la lista cerrada de lo que se sirve. SVG queda fuera a proposito: es un
// documento con scripts, no una imagen inerte.
var mediaContentTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// mediaStore es lo que el gateway necesita del almacen de objetos (objectstore.Store).
type mediaStore interface {
	Stat(ctx context.Context, key string) (objectstore.ObjectInfo, error)
	OpenLimited(ctx context.Context, key string, maxBytes int64) (io.ReadCloser, objectstore.ObjectInfo, error)
}

// publicMediaKey dice si la clave es de un objeto del espacio public/ sin salir de el: nada de
// segmentos vacios, "." ni ".." (tambien escapados) ni barras invertidas.
func publicMediaKey(key string) bool {
	if !strings.HasPrefix(key, "public/") {
		return false
	}
	decoded, err := url.PathUnescape(key)
	if err != nil || strings.Contains(decoded, `\`) {
		return false
	}
	for _, segment := range strings.Split(decoded, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// mediaHandler sirve UNICAMENTE los objetos del espacio public/ (imagenes de plantillas y
// correos): activos no confidenciales, legibles por cualquiera con el enlace y sin sesion, porque
// los piden un <img> de un cliente de correo o el proxy de imagenes de un proveedor. El gateway
// los lee del bucket privado y los entrega el mismo: el almacen no se publica, y un correo abierto
// meses despues sigue mostrando sus imagenes (una URL prefirmada caducaria).
//
// Los objetos private/ (adjuntos y demas) NO se sirven aqui: cada servicio entrega una URL
// prefirmada en su propia respuesta, acotada por la empresa autenticada.
//
// Solo se sirve lo que el almacen declara como una imagen de la lista cerrada; cualquier otra cosa
// (o un objeto por encima de mediaMaxBytes) responde 404, como una clave que no existe. La
// respuesta lleva nosniff y una CSP que no deja ejecutar nada aunque se abra como documento.
func mediaHandler(store mediaStore, logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := chi.URLParam(r, "*")
		if !publicMediaKey(key) {
			mediaNotFound(w)
			return
		}

		// HEAD y una revalidacion solo necesitan los metadatos; el cuerpo se abre cuando hace falta.
		var (
			body io.ReadCloser
			info objectstore.ObjectInfo
			err  error
		)
		if r.Method == http.MethodHead || r.Header.Get("If-None-Match") != "" {
			info, err = store.Stat(r.Context(), key)
		} else {
			body, info, err = store.OpenLimited(r.Context(), key, mediaMaxBytes)
		}
		if body != nil {
			defer body.Close()
		}
		contentType, ok := servableMedia(w, key, info, err, logger)
		if !ok {
			return
		}

		etag := mediaETag(info.ETag)
		if etag != "" && etagMatches(r.Header.Get("If-None-Match"), etag) {
			writeMediaHeaders(w.Header(), etag, "", objectstore.ObjectInfo{})
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if r.Method == http.MethodHead {
			writeMediaHeaders(w.Header(), etag, contentType, info)
			w.WriteHeader(http.StatusOK)
			return
		}
		if body == nil {
			body, info, err = store.OpenLimited(r.Context(), key, mediaMaxBytes)
			if body != nil {
				defer body.Close()
			}
			if contentType, ok = servableMedia(w, key, info, err, logger); !ok {
				return
			}
			etag = mediaETag(info.ETag)
		}
		writeMediaHeaders(w.Header(), etag, contentType, info)
		w.WriteHeader(http.StatusOK)
		if _, err := io.Copy(w, body); err != nil {
			logger.Debug("media: envio interrumpido", zap.String("key", key), zap.Error(err))
		}
	}
}

// servableMedia traduce el resultado del almacen: 404 para lo que no existe, lo que no es una
// imagen de la lista cerrada y lo que pasa del tope (sin distinguirlos hacia fuera), y 502 si el
// almacen no responde. Devuelve el tipo normalizado cuando el objeto se puede servir.
func servableMedia(w http.ResponseWriter, key string, info objectstore.ObjectInfo, err error, logger *zap.Logger) (string, bool) {
	switch {
	case errors.Is(err, objectstore.ErrNotFound):
		mediaNotFound(w)
		return "", false
	case errors.Is(err, objectstore.ErrTooLarge):
		logger.Warn("media: objeto por encima del tope", zap.String("key", key), zap.Int64("size", info.Size))
		mediaNotFound(w)
		return "", false
	case err != nil:
		logger.Warn("media: lectura del almacen fallida", zap.String("key", key), zap.Error(err))
		response.Err(w, http.StatusBadGateway, "MEDIA_UNAVAILABLE", "media unavailable")
		return "", false
	}
	contentType, ok := mediaContentType(info.ContentType)
	if !ok {
		logger.Warn("media: tipo no servible", zap.String("key", key), zap.String("content_type", info.ContentType))
		mediaNotFound(w)
		return "", false
	}
	if info.Size < 0 || info.Size > mediaMaxBytes {
		logger.Warn("media: objeto por encima del tope", zap.String("key", key), zap.Int64("size", info.Size))
		mediaNotFound(w)
		return "", false
	}
	return contentType, true
}

// writeMediaHeaders fija las cabeceras de una respuesta servida. La CSP sustituye a la de la
// aplicacion (SecureHeaders): una imagen no carga nada. Sin contentType (un 304) solo van las de
// cache y validacion.
func writeMediaHeaders(h http.Header, etag, contentType string, info objectstore.ObjectInfo) {
	h.Set("Cache-Control", mediaCacheControl)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'")
	if etag != "" {
		h.Set("ETag", etag)
	}
	if contentType == "" {
		return
	}
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.FormatInt(info.Size, 10))
	if !info.LastModified.IsZero() {
		h.Set("Last-Modified", info.LastModified.UTC().Format(http.TimeFormat))
	}
}

func mediaNotFound(w http.ResponseWriter) {
	response.Err(w, http.StatusNotFound, "NOT_FOUND", "not found")
}

// mediaContentType normaliza el tipo que guardo el almacen (sin parametros, en minusculas) y dice
// si esta en la lista cerrada.
func mediaContentType(raw string) (string, bool) {
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return "", false
	}
	return mediaType, mediaContentTypes[mediaType]
}

// mediaETag entrecomilla el ETag de S3 (hexadecimal, con "-N" si se subio por partes). Uno con
// otros caracteres no se devuelve: iria tal cual a una cabecera.
func mediaETag(raw string) string {
	if raw == "" || len(raw) > 128 {
		return ""
	}
	for _, c := range raw {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' || c == '-') {
			return ""
		}
	}
	return `"` + raw + `"`
}

// etagMatches aplica If-None-Match (RFC 9110, 13.1.2): comparacion debil, lista separada por
// comas o "*".
func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}
