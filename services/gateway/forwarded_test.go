package main

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestForwardedSchemeSoloAceptaHttpYHttps(t *testing.T) {
	for _, tc := range []struct {
		nombre string
		host   string
		header []string
		tls    bool
		want   string
	}{
		{"https del borde", "email.acme.test", []string{"https"}, false, "https"},
		{"http declarado", "email.acme.test", []string{"http"}, false, "http"},
		{"mayusculas y espacios", "email.acme.test", []string{"  HTTPS "}, false, "https"},
		{"lista: el esquema original del visitante", "email.acme.test", []string{"http, https"}, false, "http"},
		{"lista con varios valores de cabecera", "email.acme.test", []string{"https", "http"}, false, "https"},
		{"esquema ajeno se descarta", "email.acme.test", []string{"javascript"}, false, "https"},
		{"texto con inyeccion se descarta", "email.acme.test", []string{"https\r\nX-User-ID: 1"}, false, "https"},
		{"vacio", "email.acme.test", []string{""}, false, "https"},
		{"sin cabecera fuera de local", "email.acme.test", nil, false, "https"},
		{"sin cabecera en localhost", "localhost:8080", nil, false, "http"},
		{"sin cabecera en loopback", "127.0.0.1:8080", nil, false, "http"},
		{"sin cabecera en loopback IPv6", "[::1]:8080", nil, false, "http"},
		{"invalida en localhost cae al desarrollo", "localhost:8080", []string{"ftp"}, false, "http"},
		{"un nombre que solo empieza por localhost no es local", "localhost.atacante.test", nil, false, "https"},
		{"un nombre que solo empieza por 127.0.0.1 no es local", "127.0.0.1.atacante.test", nil, false, "https"},
		{"TLS propio manda sobre la cabecera", "email.acme.test", []string{"http"}, true, "https"},
	} {
		t.Run(tc.nombre, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+tc.host+"/", nil)
			req.Host = tc.host
			for _, v := range tc.header {
				req.Header.Add("X-Forwarded-Proto", v)
			}
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			if got := forwardedScheme(req); got != tc.want {
				t.Fatalf("forwardedScheme = %q, se esperaba %q", got, tc.want)
			}
		})
	}
}

// El servicio recibe siempre un valor valido, tambien cuando el cliente manda basura.
func TestElProxyNormalizaXForwardedProto(t *testing.T) {
	var got string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Forwarded-Proto")
	}))
	defer upstream.Close()
	proxy := reverseProxy(upstream.URL, "token-interno")

	for _, tc := range []struct{ enviado, want string }{
		{"HTTPS", "https"},
		{"file", "https"},
		{"http, https", "http"},
	} {
		req := httptest.NewRequest(http.MethodGet, "http://email.acme.test/", nil)
		req.Header.Set("X-Forwarded-Proto", tc.enviado)
		proxy.ServeHTTP(httptest.NewRecorder(), req)
		if got != tc.want {
			t.Fatalf("enviado %q: el servicio recibio %q, se esperaba %q", tc.enviado, got, tc.want)
		}
	}
}
