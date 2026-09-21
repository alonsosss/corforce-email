package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
)

// El proxy elimina, despues de poner sus cabeceras, las que el cliente nombre en Connection
// (net/http/httputil, ReverseProxy.Director). Sin defensa, un cliente con sesion quita por si mismo
// X-User-ID, X-Tenant-ID, X-Gateway-Token o X-Real-IP del salto hacia el servicio, y los servicios,
// que dan por hecho que esas cabeceras las escribe solo el gateway, lo tratarian como una llamada de
// otro servicio (RequireInternalCaller, internalOrPerm, Membership.Require).
func TestElClienteNoQuitaLasCabecerasInternasConConnection(t *testing.T) {
	var got http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	const (
		userID   = "7f0b1e2c-0000-4000-8000-000000000001"
		tenantID = "7f0b1e2c-0000-4000-8000-0000000000aa"
	)
	proxy := reverseProxy(upstream.URL, "token-interno")
	identificado := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), middleware.CtxUserID, userID)
		ctx = context.WithValue(ctx, middleware.CtxTenantID, tenantID)
		ctx = context.WithValue(ctx, middleware.CtxRoles, []string{"empleado"})
		proxy.ServeHTTP(w, r.WithContext(ctx))
	})
	chain := middleware.StripInternalHeaders(middleware.CaptureClientIP(middleware.TrustedProxyCIDRs(""))(identificado))

	for _, connection := range []string{
		"X-User-ID, X-Tenant-ID, X-User-Roles, X-Gateway-Token, X-Real-IP, X-Forwarded-Host, X-Forwarded-Proto",
		"x-user-id",
		"close, X-Gateway-Token",
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/access/denials", strings.NewReader("{}"))
		req.RemoteAddr = "172.31.255.2:4000"
		req.Header.Set("X-Real-IP", "203.0.113.9")
		req.Header.Set("Connection", connection)
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("Connection %q: status %d", connection, rec.Code)
		}
		for name, want := range map[string]string{
			"X-User-ID": userID, "X-Tenant-ID": tenantID, "X-User-Roles": "empleado",
			"X-Gateway-Token": "token-interno", "X-Real-IP": "203.0.113.9", "X-Forwarded-Host": "example.com",
		} {
			if got.Get(name) != want {
				t.Fatalf("Connection %q: %s = %q, se esperaba %q", connection, name, got.Get(name), want)
			}
		}
		if got.Get("X-Forwarded-Proto") == "" {
			t.Fatalf("Connection %q: falta X-Forwarded-Proto", connection)
		}
	}
}

// Los encabezados de salto que el cliente nombre siguen sin viajar al servicio (RFC 9110, 7.6.1).
func TestLosEncabezadosDeSaltoDelClienteNoLlegan(t *testing.T) {
	var got http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	proxy := reverseProxy(upstream.URL, "token-interno")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
	req.Header.Set("Connection", "X-Solo-Para-El-Proxy")
	req.Header.Set("X-Solo-Para-El-Proxy", "1")
	req.Header.Set("X-Otro", "2")
	proxy.ServeHTTP(httptest.NewRecorder(), req)
	if got.Get("X-Solo-Para-El-Proxy") != "" || got.Get("X-Otro") != "2" {
		t.Fatalf("cabeceras al servicio: %v", got)
	}
}
