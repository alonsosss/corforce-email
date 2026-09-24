package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// El enlace de un fichero compartido se abre sin sesion (GET, la pagina) y se descarga con POST, con el
// plazo de escritura ampliado; ninguna otra ruta publica lo lleva.
func TestLasRutasDeFicherosCompartidos(t *testing.T) {
	t.Setenv("GATEWAY_ROUTES_FILE", "")
	tbl, err := loadRouteTable()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]publicRouteSpec{}
	for _, p := range tbl.Public {
		if p.Transfer != "" && p.Service != "mail-files" {
			t.Errorf("%s %s amplia el plazo de escritura sin ser una descarga", p.Method, p.Path)
		}
		if p.Service == "mail-files" {
			found[p.Method] = p
		}
	}
	get, post := found[http.MethodGet], found[http.MethodPost]
	if get.Path != "/public/files/{tenant}/{file}" || get.Transfer != "" || get.Content != "" {
		t.Fatalf("pagina del enlace: %+v", get)
	}
	if post.Path != get.Path || post.Transfer != publicTransferDownload || post.Limit != "" {
		t.Fatalf("descarga: %+v", post)
	}
	if tbl.Services["mail-files"].CellHostsEnv != "" {
		t.Fatal("mail-files es de empresa, no de celda")
	}
}

func TestTransferSoloDownloadEnGetOPost(t *testing.T) {
	for _, p := range []publicRouteSpec{
		{Method: "PUT", Path: "/public/x", Service: "x", Transfer: publicTransferDownload},
		{Method: "GET", Path: "/public/x", Service: "x", Transfer: "stream"},
	} {
		if err := validatePublicExtras(p); err == nil {
			t.Errorf("%+v aceptada", p)
		}
	}
	for _, method := range []string{"GET", "POST"} {
		if err := validatePublicExtras(publicRouteSpec{Method: method, Path: "/public/x", Service: "x", Transfer: publicTransferDownload}); err != nil {
			t.Errorf("%s: %v", method, err)
		}
	}
}

// Una respuesta que tarda mas que el WriteTimeout del servidor llega entera si su ruta amplia el plazo;
// sin la ampliacion el cliente la pierde.
func TestLaDescargaSobreviveAlPlazoDelServidor(t *testing.T) {
	slow := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, "contenido")
	})
	r := chi.NewRouter()
	r.Post("/con", withDownloadDeadline(slow).ServeHTTP)
	r.Post("/sin", slow)
	srv := httptest.NewUnstartedServer(r)
	srv.Config.WriteTimeout = 100 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/con", "text/plain", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "contenido" {
		t.Fatalf("con ampliacion: %q", body)
	}
	if resp, err := http.Post(srv.URL+"/sin", "text/plain", strings.NewReader("")); err == nil {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if string(body) == "contenido" {
			t.Fatal("sin ampliacion la respuesta tambien llego: la prueba no mide nada")
		}
	}
}
