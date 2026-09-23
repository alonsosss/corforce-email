package objectstore

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// s3Falso responde como S3 a HEAD y GET de objeto en el bucket "medios": lo justo para ejercitar
// Stat y OpenLimited con el cliente real (firma, cabeceras y errores XML), sin red ni MinIO.
type s3Falso struct {
	objetos map[string]string
	tipos   map[string]string
	// declarado permite que la cabecera Content-Length mienta sobre el cuerpo.
	declarado map[string]int
	fallo     int
}

func (f *s3Falso) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.fallo != 0 {
		w.WriteHeader(f.fallo)
		return
	}
	clave := strings.TrimPrefix(r.URL.Path, "/medios/")
	cuerpo, ok := f.objetos[clave]
	if !ok {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>no existe</Message><Key>`+clave+`</Key></Error>`)
		}
		return
	}
	largo := len(cuerpo)
	if n, ok := f.declarado[clave]; ok {
		largo = n
	}
	w.Header().Set("Content-Type", f.tipos[clave])
	w.Header().Set("Content-Length", strconv.Itoa(largo))
	w.Header().Set("ETag", `"0123456789abcdef0123456789abcdef"`)
	w.Header().Set("Last-Modified", "Wed, 23 Sep 2026 10:00:00 GMT")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, cuerpo)
	}
}

func almacenFalso(t *testing.T, f *s3Falso) *Store {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	st, err := New(Config{
		Endpoint:  strings.TrimPrefix(srv.URL, "http://"),
		AccessKey: "clave-de-prueba",
		SecretKey: "secreto-de-prueba-de-cuarenta-caracteres",
		Bucket:    "medios",
		// Con region el cliente no pregunta la ubicacion del bucket.
		Region: "us-east-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestStatDevuelveLosMetadatosSinComillasEnElETag(t *testing.T) {
	st := almacenFalso(t, &s3Falso{
		objetos: map[string]string{"public/t/a.png": "png!"},
		tipos:   map[string]string{"public/t/a.png": "image/png"},
	})
	info, err := st.Stat(context.Background(), "public/t/a.png")
	if err != nil {
		t.Fatal(err)
	}
	if info.ContentType != "image/png" || info.Size != 4 || info.ETag != "0123456789abcdef0123456789abcdef" || info.LastModified.IsZero() {
		t.Fatalf("metadatos inesperados: %+v", info)
	}
}

func TestStatYOpenLimitedDistinguenLaClaveQueNoExiste(t *testing.T) {
	st := almacenFalso(t, &s3Falso{objetos: map[string]string{}})
	if _, err := st.Stat(context.Background(), "public/t/nada.png"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat de una clave inexistente: %v, se esperaba ErrNotFound", err)
	}
	body, _, err := st.OpenLimited(context.Background(), "public/t/nada.png", 1<<20)
	if !errors.Is(err, ErrNotFound) || body != nil {
		t.Fatalf("OpenLimited de una clave inexistente: %v, se esperaba ErrNotFound sin cuerpo", err)
	}
}

func TestOpenLimitedEntregaElObjeto(t *testing.T) {
	st := almacenFalso(t, &s3Falso{
		objetos: map[string]string{"public/t/a.gif": "GIF89a"},
		tipos:   map[string]string{"public/t/a.gif": "image/gif"},
	})
	body, info, err := st.OpenLimited(context.Background(), "public/t/a.gif", 6)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "GIF89a" || info.Size != 6 || info.ContentType != "image/gif" {
		t.Fatalf("contenido %q, info %+v", got, info)
	}
}

// Un objeto que declara mas del tope se rechaza antes de entregar un solo byte.
func TestOpenLimitedRechazaLoQueSuperaElTope(t *testing.T) {
	st := almacenFalso(t, &s3Falso{
		objetos: map[string]string{"public/t/grande.png": strings.Repeat("x", 11)},
		tipos:   map[string]string{"public/t/grande.png": "image/png"},
	})
	body, info, err := st.OpenLimited(context.Background(), "public/t/grande.png", 10)
	if !errors.Is(err, ErrTooLarge) || body != nil {
		t.Fatalf("objeto de 11 bytes con tope 10: %v, se esperaba ErrTooLarge sin cuerpo", err)
	}
	if info.Size != 11 {
		t.Fatalf("el error debe traer el tamano declarado para registrarlo: %+v", info)
	}
}

// El lector no entrega mas de lo que el objeto declaro, aunque el cuerpo traiga mas bytes.
func TestOpenLimitedNoLeeMasDeLoDeclarado(t *testing.T) {
	st := almacenFalso(t, &s3Falso{
		objetos:   map[string]string{"public/t/b.png": "12345678"},
		tipos:     map[string]string{"public/t/b.png": "image/png"},
		declarado: map[string]int{"public/t/b.png": 4},
	})
	body, info, err := st.OpenLimited(context.Background(), "public/t/b.png", 100)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, _ := io.ReadAll(body)
	if info.Size != 4 || len(got) > 4 {
		t.Fatalf("declarado %d, leidos %d bytes (%q)", info.Size, len(got), got)
	}
}

func TestErrorDelAlmacenNoSeConfundeConNoEncontrado(t *testing.T) {
	st := almacenFalso(t, &s3Falso{fallo: http.StatusForbidden})
	_, err := st.Stat(context.Background(), "public/t/a.png")
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrTooLarge) {
		t.Fatalf("un 403 del almacen es un fallo, no una clave inexistente: %v", err)
	}
}
