package rspamd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

// checkv2JSON es una respuesta real de /checkv2 de Rspamd 3.x (recortada a unos simbolos) para un correo
// HTML sin sobre SMTP: la forma de la de 4.1.4, con metric_score, opciones y cabeceras del milter.
const checkv2JSON = `{"is_skipped":false,"score":6.92,"required_score":15.0,"action":"no action",
 "thresholds":{"reject":15.0,"add header":8.0,"greylist":7.0},
 "symbols":{
  "MIME_HTML_ONLY":{"name":"MIME_HTML_ONLY","score":0.2,"metric_score":0.2,"description":"Messages that have only HTML part"},
  "MISSING_MID":{"name":"MISSING_MID","score":2.5,"metric_score":2.5,"description":"Message id is missing"},
  "R_SUSPICIOUS_URL":{"name":"R_SUSPICIOUS_URL","score":5.0,"metric_score":5.0,"description":"Obfusicated or suspicious URL has been found in a message","options":["bit.ly"]},
  "MIME_TRACE":{"name":"MIME_TRACE","score":0.0,"metric_score":0.0,"options":["0:~"]},
  "ARC_NA":{"name":"ARC_NA","score":0.0,"metric_score":0.0,"description":"ARC signature absent"},
  "BAYES_HAM":{"name":"BAYES_HAM","score":-0.78,"metric_score":-3.0,"description":"Message probably ham, probability: ","options":["97.21%"]},
  "RCVD_COUNT_ZERO":{"name":"RCVD_COUNT_ZERO","score":0.0,"metric_score":0.0,"description":"Message has no Received headers","options":["0"]}
 },
 "messages":{},"message-id":"undef","time_real":0.183621,
 "milter":{"remove_headers":{"X-Spam":0}}}`

const mimePlantilla = "From: Acme <ventas@acme.test>\r\nTo: cliente@ejemplo.org\r\nSubject: Oferta\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n\r\n<p>Visita <a href=\"https://bit.ly/x\">la tienda</a></p>\r\n"

type escaneo struct {
	method, path, rawQuery, password, flags, queueID, contentType, body string
}

func rspamdQuePuntua(t *testing.T, status int, reply string) (*Client, *[]escaneo) {
	t.Helper()
	var seen []escaneo
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, escaneo{method: r.Method, path: r.URL.Path, rawQuery: r.URL.RawQuery, password: r.Header.Get("Password"),
			flags: r.Header.Get("Flags"), queueID: r.Header.Get("Queue-Id"), contentType: r.Header.Get("Content-Type"), body: string(body)})
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(ts.Close)
	return New(ts.URL, "lectura-secreta"), &seen
}

func TestLaPuntuacionUsaCheckv2ConLaContrasenaDeLecturaYSinDejarHuella(t *testing.T) {
	c, seen := rspamdQuePuntua(t, http.StatusOK, checkv2JSON)
	got, err := c.Check(context.Background(), []byte(mimePlantilla))
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 {
		t.Fatalf("peticiones: %d", len(*seen))
	}
	req := (*seen)[0]
	if req.method != http.MethodPost || req.path != "/checkv2" || req.rawQuery != "" || req.password != "lectura-secreta" {
		t.Fatalf("peticion: %+v", req)
	}
	if req.body != mimePlantilla || req.contentType != "message/rfc822" {
		t.Fatalf("cuerpo o tipo: %q %q", req.contentType, req.body)
	}
	// Sin Queue-Id el bayesiano no aprende solo; no_log y no_stat lo dejan fuera del historial y de /stat.
	if req.queueID != "" || !strings.Contains(req.flags, "no_log") || !strings.Contains(req.flags, "no_stat") {
		t.Fatalf("huella: queue-id %q flags %q", req.queueID, req.flags)
	}
	if got.Score.String() != "6.92" || got.Required.String() != "15" || got.Action != "no action" {
		t.Fatalf("veredicto: %s %s %s", got.Score, got.Required, got.Action)
	}
	var names []string
	for _, s := range got.Symbols {
		names = append(names, s.Name+"="+s.Score.String())
	}
	want := "R_SUSPICIOUS_URL=5,MISSING_MID=2.5,MIME_HTML_ONLY=0.2,ARC_NA=0,MIME_TRACE=0,RCVD_COUNT_ZERO=0,BAYES_HAM=-0.78"
	if strings.Join(names, ",") != want {
		t.Fatalf("simbolos:\n got %s\nwant %s", strings.Join(names, ","), want)
	}
	if got.Symbols[0].Description != "Obfusicated or suspicious URL has been found in a message" || got.Symbols[4].Description != "" {
		t.Fatalf("descripciones: %+v", got.Symbols)
	}
}

func TestLaPuntuacionSinContrasenaNoLlamaARspamd(t *testing.T) {
	var calls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	t.Cleanup(ts.Close)
	if _, err := New(ts.URL, "").Check(context.Background(), []byte(mimePlantilla)); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("sin contrasena: %v", err)
	}
	if calls != 0 {
		t.Fatalf("llamadas: %d", calls)
	}
}

func TestLosFallosDeLaPuntuacionSeClasificanSinRepetirLaContrasenaNiElMensaje(t *testing.T) {
	cases := []struct {
		name   string
		status int
		reply  string
		want   error
	}{
		{"contrasena rechazada", http.StatusForbidden, `{"error":"Unauthorized"}`, domain.ErrNotConfigured},
		{"sin autenticar", http.StatusUnauthorized, ``, domain.ErrNotConfigured},
		{"motor roto", http.StatusInternalServerError, `{"error":"task timeout"}`, domain.ErrEngineUnreachable},
		{"peticion rechazada", http.StatusBadRequest, `{"error":"Oferta: cannot parse"}`, domain.ErrEngineCommand},
		{"ilegible", http.StatusOK, `<html>`, domain.ErrEngineCommand},
		{"sin veredicto", http.StatusOK, `{"error":"Oferta: message has no content"}`, domain.ErrEngineCommand},
		{"demasiado grande", http.StatusOK, `{"action":"no action","pad":"` + strings.Repeat("x", maxCheckBody) + `"}`, domain.ErrEngineCommand},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := rspamdQuePuntua(t, tc.status, tc.reply)
			_, err := c.Check(context.Background(), []byte(mimePlantilla))
			if !errors.Is(err, tc.want) {
				t.Fatalf("error: %v", err)
			}
			if strings.Contains(err.Error(), "lectura-secreta") || strings.Contains(err.Error(), "Oferta") {
				t.Fatalf("el error repite la contrasena o el mensaje: %v", err)
			}
		})
	}

	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	ts.Close()
	if _, err := New(ts.URL, "lectura-secreta").Check(context.Background(), []byte(mimePlantilla)); !errors.Is(err, domain.ErrEngineUnreachable) {
		t.Fatalf("rspamd caido: %v", err)
	}
}
