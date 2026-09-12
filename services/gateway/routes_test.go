package main

import (
	"encoding/json"
	"testing"
)

// La tabla embebida es la que arranca en produccion: si deja de validar, el gateway
// no levanta. Se comprueba aqui para que el fallo aparezca en CI y no en el despliegue.
func TestTablaEmbebidaEsValida(t *testing.T) {
	var tbl routeTable
	if err := json.Unmarshal(defaultRoutes, &tbl); err != nil {
		t.Fatalf("routes.json no es JSON valido: %v", err)
	}
	if err := tbl.validate(); err != nil {
		t.Fatalf("routes.json no valida: %v", err)
	}
	idx := tbl.moduleIndex()
	for _, quiere := range []string{"users", "organizations", "audit"} {
		if idx[quiere] == "" {
			t.Errorf("la ruta %q debe gatearse por modulo", quiere)
		}
	}
	// Las consultas de acceso las resuelve access-control con el JWT: gatearlas por
	// modulo dejaria sin menu a quien todavia no tiene ninguno.
	if idx["access"] != "" {
		t.Errorf("la ruta access no debe gatearse por modulo")
	}
}

func TestValidacionRechazaIncoherencias(t *testing.T) {
	base := func() routeTable {
		return routeTable{
			Services: map[string]serviceSpec{"identity": {HostEnv: "IDENTITY_HOST", DefaultHost: "identity", DefaultPort: "8001"}},
			Routes:   []routeSpec{{Prefix: "users", Service: "identity", Module: "identity"}},
		}
	}
	casos := map[string]func(*routeTable){
		"servicio desconocido": func(t *routeTable) { t.Routes[0].Service = "nadie" },
		"prefijo repetido":     func(t *routeTable) { t.Routes = append(t.Routes, t.Routes[0]) },
		"prefijo invalido":     func(t *routeTable) { t.Routes[0].Prefix = "Users" },
		"modulo invalido":      func(t *routeTable) { t.Routes[0].Module = "mail-boxes" },
		"frontend desconocido": func(t *routeTable) { t.Frontend = "web" },
		"host_env invalido":    func(t *routeTable) { s := t.Services["identity"]; s.HostEnv = "identity_host"; t.Services["identity"] = s },
	}
	for nombre, romper := range casos {
		tbl := base()
		romper(&tbl)
		if err := tbl.validate(); err == nil {
			t.Errorf("%s: se esperaba error de validacion", nombre)
		}
	}
	tbl := base()
	if err := tbl.validate(); err != nil {
		t.Fatalf("la tabla base debe validar: %v", err)
	}
}

func TestServiceURLPrefiereElEntorno(t *testing.T) {
	tbl := routeTable{Services: map[string]serviceSpec{"identity": {HostEnv: "IDENTITY_HOST", DefaultHost: "identity", DefaultPort: "8001"}}}
	if got := tbl.serviceURL("identity"); got != "http://identity:8001" {
		t.Fatalf("sin entorno: %s", got)
	}
	t.Setenv("IDENTITY_HOST", "10.0.0.5")
	t.Setenv("IDENTITY_HOST_PORT", "9001")
	if got := tbl.serviceURL("identity"); got != "http://10.0.0.5:9001" {
		t.Fatalf("con entorno: %s", got)
	}
}
