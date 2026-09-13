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
	// El webmail lo autentica el propio servicio (buzones, no usuarios de la plataforma)
	// y su inicio de sesion va con el limitador estricto.
	var webmail *selfAuthSpec
	for i := range tbl.SelfAuthenticated {
		if tbl.SelfAuthenticated[i].Prefix == "webmail" {
			webmail = &tbl.SelfAuthenticated[i]
		}
	}
	if webmail == nil || webmail.Service != "webmail" {
		t.Fatalf("el webmail debe declararse en self_authenticated")
	}
	if len(webmail.StrictLimit) != 1 || webmail.StrictLimit[0] != (methodPathSpec{Method: "POST", Path: "/session"}) {
		t.Errorf("el inicio de sesion del webmail debe ir con el limitador estricto: %+v", webmail.StrictLimit)
	}
	if idx["webmail"] != "" {
		t.Errorf("el webmail no se gatea por modulo")
	}

	// Los enlaces del aviso de cuarentena se enrutan por celda; los de antes, sin celda, van
	// a la celda por defecto hasta que caduquen.
	if tbl.Services["mail-security"].CellHostsEnv != "MAIL_SECURITY_CELL_HOSTS" {
		t.Fatalf("mail-security es un servicio de celda: %+v", tbl.Services["mail-security"])
	}
	public := map[publicRouteSpec]bool{}
	for _, p := range tbl.Public {
		public[p] = true
	}
	for _, method := range []string{"GET", "POST"} {
		for _, action := range []string{"release", "discard"} {
			byCell := publicRouteSpec{Method: method, Path: "/public/mail-security/quarantine/{cell}/" + action, Service: "mail-security"}
			legacy := publicRouteSpec{Method: method, Path: "/public/mail-security/quarantine/" + action, Service: "mail-security", DefaultCell: true}
			if !public[byCell] || !public[legacy] {
				t.Errorf("%s %s: faltan la ruta por celda o la de antes a la celda por defecto", method, action)
			}
		}
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
		"host_env invalido": func(t *routeTable) {
			s := t.Services["identity"]
			s.HostEnv = "identity_host"
			t.Services["identity"] = s
		},
		"publica fuera de /public": func(t *routeTable) {
			t.Public = []publicRouteSpec{{Method: "POST", Path: "/auth/login", Service: "identity"}}
		},
		"publica con metodo raro": func(t *routeTable) {
			t.Public = []publicRouteSpec{{Method: "PATCH", Path: "/public/x", Service: "identity"}}
		},
		"autenticada por el servicio sobre una ruta con JWT": func(t *routeTable) {
			t.SelfAuthenticated = []selfAuthSpec{{Prefix: "users", Service: "identity"}}
		},
		"autenticada por el servicio sobre un prefijo reservado": func(t *routeTable) {
			t.SelfAuthenticated = []selfAuthSpec{{Prefix: "auth", Service: "identity"}}
		},
		"autenticada por el servicio repetida": func(t *routeTable) {
			t.SelfAuthenticated = []selfAuthSpec{{Prefix: "inbox", Service: "identity"}, {Prefix: "inbox", Service: "identity"}}
		},
		"autenticada por el servicio desconocido": func(t *routeTable) {
			t.SelfAuthenticated = []selfAuthSpec{{Prefix: "inbox", Service: "nadie"}}
		},
		"autenticada por el servicio con prefijo invalido": func(t *routeTable) {
			t.SelfAuthenticated = []selfAuthSpec{{Prefix: "In/box", Service: "identity"}}
		},
		"limite estricto con metodo raro": func(t *routeTable) {
			t.SelfAuthenticated = []selfAuthSpec{{Prefix: "inbox", Service: "identity", StrictLimit: []methodPathSpec{{Method: "TRACE", Path: "/session"}}}}
		},
		"limite estricto con ruta relativa": func(t *routeTable) {
			t.SelfAuthenticated = []selfAuthSpec{{Prefix: "inbox", Service: "identity", StrictLimit: []methodPathSpec{{Method: "POST", Path: "session"}}}}
		},
		"ruta por celda de un servicio que no es de celda": func(t *routeTable) {
			t.Public = []publicRouteSpec{{Method: "GET", Path: "/public/x/{cell}/y", Service: "identity"}}
		},
		"ruta de un servicio de celda sin celda ni default_cell": func(t *routeTable) {
			cellService(t)
			t.Public = append(t.Public, publicRouteSpec{Method: "GET", Path: "/public/q/release", Service: "mail-security"})
		},
		"default_cell en una ruta con celda": func(t *routeTable) {
			cellService(t)
			t.Public[0].DefaultCell = true
		},
		"default_cell en un servicio que no es de celda": func(t *routeTable) {
			t.Public = []publicRouteSpec{{Method: "GET", Path: "/public/x", Service: "identity", DefaultCell: true}}
		},
		"celda dos veces": func(t *routeTable) {
			cellService(t)
			t.Public[0].Path = "/public/q/{cell}/{cell}/release"
		},
		"celda dentro de un segmento": func(t *routeTable) {
			cellService(t)
			t.Public[0].Path = "/public/q/c-{cell}/release"
		},
		"celda con expresion": func(t *routeTable) {
			cellService(t)
			t.Public[0].Path = "/public/q/{cell:[a-z]+}/release"
		},
		"cell_hosts_env sin ruta por celda": func(t *routeTable) {
			cellService(t)
			t.Public = nil
		},
		"cell_hosts_env con forma invalida": func(t *routeTable) {
			cellService(t)
			s := t.Services["mail-security"]
			s.CellHostsEnv = "mail_security_cells"
			t.Services["mail-security"] = s
		},
		"cell_hosts_env que pisa el puerto de un servicio": func(t *routeTable) {
			cellService(t)
			s := t.Services["mail-security"]
			s.CellHostsEnv = "IDENTITY_HOST_PORT"
			t.Services["mail-security"] = s
		},
		"cell_hosts_env repetido": func(t *routeTable) {
			cellService(t)
			t.Services["webmail"] = serviceSpec{HostEnv: "WEBMAIL_HOST", DefaultHost: "webmail", DefaultPort: "8044", CellHostsEnv: "MAIL_SECURITY_CELL_HOSTS"}
			t.Public = append(t.Public, publicRouteSpec{Method: "GET", Path: "/public/w/{cell}/x", Service: "webmail"})
		},
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
	tbl = base()
	cellService(&tbl)
	if err := tbl.validate(); err != nil {
		t.Fatalf("la tabla con un servicio de celda debe validar: %v", err)
	}
}

// cellService anade un servicio de celda con una ruta por celda y la de antes.
func cellService(t *routeTable) {
	t.Services["mail-security"] = serviceSpec{HostEnv: "MAIL_SECURITY_HOST", DefaultHost: "mail-security", DefaultPort: "8042", CellHostsEnv: "MAIL_SECURITY_CELL_HOSTS"}
	t.Public = []publicRouteSpec{
		{Method: "GET", Path: "/public/q/{cell}/release", Service: "mail-security"},
		{Method: "GET", Path: "/public/q/release", Service: "mail-security", DefaultCell: true},
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
