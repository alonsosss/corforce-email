package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/objectstore"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

type fakeObject struct {
	info objectstore.ObjectInfo
	body []byte
}

// fakeMediaStore imita objectstore.Store: Stat y OpenLimited con el mismo contrato de errores, y
// cuenta las llamadas para comprobar que HEAD y una revalidacion no abren el contenido.
type fakeMediaStore struct {
	objects map[string]fakeObject
	failure error
	stats   int
	opens   int
	closed  int
}

func (f *fakeMediaStore) Stat(_ context.Context, key string) (objectstore.ObjectInfo, error) {
	f.stats++
	if f.failure != nil {
		return objectstore.ObjectInfo{}, f.failure
	}
	o, ok := f.objects[key]
	if !ok {
		return objectstore.ObjectInfo{}, fmt.Errorf("%w: %q", objectstore.ErrNotFound, key)
	}
	return o.info, nil
}

func (f *fakeMediaStore) OpenLimited(_ context.Context, key string, maxBytes int64) (io.ReadCloser, objectstore.ObjectInfo, error) {
	f.opens++
	if f.failure != nil {
		return nil, objectstore.ObjectInfo{}, f.failure
	}
	o, ok := f.objects[key]
	if !ok {
		return nil, objectstore.ObjectInfo{}, fmt.Errorf("%w: %q", objectstore.ErrNotFound, key)
	}
	if o.info.Size > maxBytes {
		return nil, o.info, fmt.Errorf("%w: %q", objectstore.ErrTooLarge, key)
	}
	return &countingCloser{Reader: bytes.NewReader(o.body), onClose: func() { f.closed++ }}, o.info, nil
}

type countingCloser struct {
	io.Reader
	onClose func()
}

func (c *countingCloser) Close() error { c.onClose(); return nil }

const pngKey = "public/7f0c/templates/3a7bd3e2360a3d29eea436fcfb7e44c735d117c42d1c1835420b6b9942dd4f1b.png"

func newFakeMediaStore() *fakeMediaStore {
	png := []byte("\x89PNG\r\n\x1a\nimagen")
	return &fakeMediaStore{objects: map[string]fakeObject{
		pngKey: {
			info: objectstore.ObjectInfo{Key: pngKey, ContentType: "image/png", Size: int64(len(png)),
				ETag: "9b2cf535f27731c974343645a3985328", LastModified: time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)},
			body: png,
		},
		"public/7f0c/templates/foto.jpg":      {info: objectstore.ObjectInfo{ContentType: "IMAGE/JPEG; charset=binary", Size: 3, ETag: "aa"}, body: []byte("jpg")},
		"public/7f0c/templates/dibujo.svg":    {info: objectstore.ObjectInfo{ContentType: "image/svg+xml", Size: 5, ETag: "bb"}, body: []byte("<svg>")},
		"public/7f0c/templates/pagina.html":   {info: objectstore.ObjectInfo{ContentType: "text/html", Size: 6, ETag: "cc"}, body: []byte("<html>")},
		"public/7f0c/templates/sin-tipo.png":  {info: objectstore.ObjectInfo{ContentType: "", Size: 3, ETag: "dd"}, body: []byte("png")},
		"public/7f0c/templates/enorme.png":    {info: objectstore.ObjectInfo{ContentType: "image/png", Size: mediaMaxBytes + 1, ETag: "ee"}},
		"public/7f0c/templates/etag-raro.gif": {info: objectstore.ObjectInfo{ContentType: "image/gif", Size: 3, ETag: "a\"\r\nX-Inyectada: 1"}, body: []byte("gif")},
	}}
}

func mediaRouter(store mediaStore) http.Handler {
	r := chi.NewRouter()
	h := mediaHandler(store, zap.NewNop())
	r.Get("/media/*", h)
	r.Head("/media/*", h)
	return r
}

func serveMedia(t *testing.T, h http.Handler, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// El gateway lee del bucket privado sin sesion, asi que solo sirve claves del espacio public/. Una
// clave que sale de el con segmentos ".." (o con barra invertida) no llega nunca al almacen: se
// interpretaria como un objeto de otro espacio.
func TestMediaNoLeeClavesQueSalenDeSuEspacio(t *testing.T) {
	store := newFakeMediaStore()
	h := mediaRouter(store)
	for _, path := range []string{
		"/media/private/tenant/adjunto.pdf",
		"/media/publicx/logo.png",
		"/media/public/../private/tenant/adjunto.pdf",
		"/media/public/%2e%2e/private/tenant/adjunto.pdf",
		"/media/public/a/./b.png",
		"/media/public//b.png",
		"/media/public/a%5Cb.png",
	} {
		if rec := serveMedia(t, h, http.MethodGet, path, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, se esperaba 404", path, rec.Code)
		}
	}
	if store.stats+store.opens != 0 {
		t.Fatalf("el almacen se consulto %d veces con claves fuera de public/", store.stats+store.opens)
	}
}

func TestMediaSirveLaImagenDelAlmacenConCacheInmutable(t *testing.T) {
	store := newFakeMediaStore()
	rec := serveMedia(t, mediaRouter(store), http.MethodGet, "/media/"+pngKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("estado %d, se esperaba 200: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Fatalf("redirige a %q: la imagen tiene que servirla el gateway", loc)
	}
	want := map[string]string{
		"Content-Type":            "image/png",
		"Content-Length":          fmt.Sprint(len(store.objects[pngKey].body)),
		"Cache-Control":           "public, max-age=31536000, immutable",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "default-src 'none'",
		"ETag":                    `"9b2cf535f27731c974343645a3985328"`,
		"Last-Modified":           "Wed, 23 Sep 2026 10:00:00 GMT",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, se esperaba %q", k, got, v)
		}
	}
	if !bytes.Equal(rec.Body.Bytes(), store.objects[pngKey].body) {
		t.Fatalf("cuerpo %q distinto del objeto", rec.Body.Bytes())
	}
	if store.opens != 1 || store.stats != 0 || store.closed != 1 {
		t.Fatalf("GET: %d aperturas, %d consultas de metadatos, %d cierres; se esperaba 1, 0 y 1", store.opens, store.stats, store.closed)
	}
}

func TestMediaNormalizaElTipoDeLaListaCerrada(t *testing.T) {
	rec := serveMedia(t, mediaRouter(newFakeMediaStore()), http.MethodGet, "/media/public/7f0c/templates/foto.jpg", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("estado %d, Content-Type %q; se esperaba 200 e image/jpeg", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// Solo PNG, JPEG, GIF y WebP: un SVG (documento con scripts), un HTML, un objeto sin tipo o uno por
// encima del tope responden como una clave que no existe, sin cabeceras de cache.
func TestMediaNiegaLoQueNoEsUnaImagenServible(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		store := newFakeMediaStore()
		h := mediaRouter(store)
		for _, name := range []string{"dibujo.svg", "pagina.html", "sin-tipo.png", "enorme.png", "no-existe.png"} {
			rec := serveMedia(t, h, method, "/media/public/7f0c/templates/"+name, nil)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s: %d, se esperaba 404", method, name, rec.Code)
			}
			if cc := rec.Header().Get("Cache-Control"); cc == mediaCacheControl {
				t.Errorf("%s %s: un 404 no puede quedar en cache para siempre", method, name)
			}
			if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "image/") || strings.HasPrefix(ct, "text/html") {
				t.Errorf("%s %s: Content-Type %q en una respuesta denegada", method, name, ct)
			}
		}
		// svg, html y sin-tipo se abren y se cierran sin servirse; enorme y no-existe no dan cuerpo.
		if method == http.MethodGet && store.closed != 3 {
			t.Errorf("GET: %d aperturas y %d cierres: todo cuerpo abierto se cierra", store.opens, store.closed)
		}
	}
}

func TestMediaHeadDevuelveCabecerasSinCuerpoNiAbrirElObjeto(t *testing.T) {
	store := newFakeMediaStore()
	rec := serveMedia(t, mediaRouter(store), http.MethodHead, "/media/"+pngKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD: %d, se esperaba 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD con cuerpo de %d bytes", rec.Body.Len())
	}
	if rec.Header().Get("Content-Length") != fmt.Sprint(len(store.objects[pngKey].body)) ||
		rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("ETag") == "" {
		t.Fatalf("HEAD sin las cabeceras del objeto: %v", rec.Header())
	}
	if store.opens != 0 || store.stats != 1 {
		t.Fatalf("HEAD: %d aperturas y %d consultas; se esperaba solo la consulta de metadatos", store.opens, store.stats)
	}
}

func TestMediaIfNoneMatchCoincidenteResponde304SinCuerpo(t *testing.T) {
	etag := `"9b2cf535f27731c974343645a3985328"`
	for _, inm := range []string{etag, "W/" + etag, `"otro", ` + etag, "*"} {
		store := newFakeMediaStore()
		rec := serveMedia(t, mediaRouter(store), http.MethodGet, "/media/"+pngKey, map[string]string{"If-None-Match": inm})
		if rec.Code != http.StatusNotModified {
			t.Fatalf("If-None-Match %s: %d, se esperaba 304", inm, rec.Code)
		}
		if rec.Body.Len() != 0 || rec.Header().Get("ETag") != etag || rec.Header().Get("Cache-Control") != mediaCacheControl {
			t.Fatalf("If-None-Match %s: 304 con cuerpo o sin ETag/Cache-Control: %v", inm, rec.Header())
		}
		if store.opens != 0 {
			t.Fatalf("If-None-Match %s: se abrio el objeto para responder 304", inm)
		}
	}
}

func TestMediaIfNoneMatchDistintoSirveElObjeto(t *testing.T) {
	store := newFakeMediaStore()
	rec := serveMedia(t, mediaRouter(store), http.MethodGet, "/media/"+pngKey, map[string]string{"If-None-Match": `"viejo"`})
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), store.objects[pngKey].body) {
		t.Fatalf("If-None-Match distinto: %d %q, se esperaba 200 con la imagen", rec.Code, rec.Body.Bytes())
	}
	if store.stats != 1 || store.opens != 1 || store.closed != 1 {
		t.Fatalf("%d consultas, %d aperturas, %d cierres; se esperaba 1, 1 y 1", store.stats, store.opens, store.closed)
	}
}

// Un ETag con caracteres ajenos al hexadecimal de S3 no se copia a la cabecera: la respuesta sale
// sin ETag (y sin 304 posible), nunca con una cabecera inyectada.
func TestMediaNoCopiaUnETagConCaracteresAjenos(t *testing.T) {
	rec := serveMedia(t, mediaRouter(newFakeMediaStore()), http.MethodGet, "/media/public/7f0c/templates/etag-raro.gif",
		map[string]string{"If-None-Match": "*"})
	if rec.Code != http.StatusOK {
		t.Fatalf("estado %d, se esperaba 200", rec.Code)
	}
	if rec.Header().Get("ETag") != "" || rec.Header().Get("X-Inyectada") != "" {
		t.Fatalf("ETag copiado del almacen sin validar: %v", rec.Header())
	}
}

func TestMediaAlmacenCaidoResponde502(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		store := newFakeMediaStore()
		store.failure = errors.New("connection refused")
		rec := serveMedia(t, mediaRouter(store), method, "/media/"+pngKey, nil)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("%s con el almacen caido: %d, se esperaba 502", method, rec.Code)
		}
		if rec.Header().Get("Cache-Control") == mediaCacheControl {
			t.Fatalf("%s: un 502 no puede quedar en cache para siempre", method)
		}
	}
}

func TestEtagMatches(t *testing.T) {
	etag := `"abc"`
	cases := map[string]bool{
		`"abc"`:          true,
		`W/"abc"`:        true,
		` "x" , "abc" `:  true,
		"*":              true,
		`"abcd"`:         false,
		`abc`:            false,
		"":               false,
		`"ABC"`:          false,
		`"x", W/"other"`: false,
	}
	for header, want := range cases {
		if got := etagMatches(header, etag); got != want {
			t.Errorf("etagMatches(%q) = %v, se esperaba %v", header, got, want)
		}
	}
}

func TestMediaETag(t *testing.T) {
	cases := map[string]string{
		"9b2cf535f27731c974343645a3985328":   `"9b2cf535f27731c974343645a3985328"`,
		"9b2cf535f27731c974343645a3985328-3": `"9b2cf535f27731c974343645a3985328-3"`,
		"":                                   "",
		`a"b`:                                "",
		"a b":                                "",
		strings.Repeat("a", 129):             "",
	}
	for raw, want := range cases {
		if got := mediaETag(raw); got != want {
			t.Errorf("mediaETag(%q) = %q, se esperaba %q", raw, got, want)
		}
	}
}
