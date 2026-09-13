package main

import (
	"testing"
)

// Una clave que la tabla no conoce (mal escrita o retirada, como default_cell) no se ignora:
// el gateway no arranca, en vez de enrutar distinto de lo que dice el fichero.
func TestTablaRechazaCamposDesconocidos(t *testing.T) {
	casos := map[string]string{
		"clave retirada en una ruta publica": `{"services":{},"public":[{"method":"GET","path":"/x","service":"s","default_cell":true}]}`,
		"clave desconocida arriba":           `{"services":{},"rutas":[]}`,
		"contenido despues del objeto":       `{"services":{}} {"services":{}}`,
	}
	for nombre, raw := range casos {
		if _, err := decodeRouteTable([]byte(raw)); err == nil {
			t.Errorf("%s: se acepto %s", nombre, raw)
		}
	}
	if _, err := decodeRouteTable([]byte(`{"services":{}}`)); err != nil {
		t.Errorf("una tabla sin claves desconocidas debe leerse: %v", err)
	}
}

// La tabla embebida es la que arranca en produccion: si deja de validar, el gateway
// no levanta. Se comprueba aqui para que el fallo aparezca en CI y no en el despliegue.
func TestTablaEmbebidaEsValida(t *testing.T) {
	tbl, err := decodeRouteTable(defaultRoutes)
	if err != nil {
		t.Fatalf("routes.json no se lee con la decodificacion estricta: %v", err)
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
	// El webmail es de celda: su inicio de sesion se enruta por el dominio del buzon y el resto
	// por la celda del token de su cookie.
	if webmail.CellLogin == nil || *webmail.CellLogin != (cellLoginSpec{Method: "POST", Path: "/session", UsernameField: "username"}) || webmail.CellCookie != "cf_wm" {
		t.Errorf("enrutado por celda del webmail: %+v %q", webmail.CellLogin, webmail.CellCookie)
	}

	// Los servicios cuya base es la celda se enrutan por celda en todas sus rutas.
	celda := map[string]string{}
	for name, s := range tbl.Services {
		if s.CellHostsEnv != "" {
			celda[name] = s.CellHostsEnv
		}
	}
	if len(celda) != 3 || celda["mail-security"] != "MAIL_SECURITY_CELL_HOSTS" || celda["mail-directory"] != "MAIL_DIRECTORY_CELL_HOSTS" ||
		celda["webmail"] != "WEBMAIL_CELL_HOSTS" {
		t.Fatalf("servicios de celda: %v", celda)
	}

	// Los enlaces del aviso de cuarentena son las unicas rutas publicas por celda.
	var quarantine []publicRouteSpec
	for _, p := range tbl.Public {
		if p.Service == "mail-security" {
			quarantine = append(quarantine, p)
		}
	}
	want := map[publicRouteSpec]bool{}
	for _, method := range []string{"GET", "POST"} {
		for _, action := range []string{"release", "discard"} {
			want[publicRouteSpec{Method: method, Path: "/public/mail-security/quarantine/{cell}/" + action, Service: "mail-security"}] = true
		}
	}
	if len(quarantine) != len(want) {
		t.Fatalf("rutas publicas de mail-security: %+v", quarantine)
	}
	for _, p := range quarantine {
		if !want[p] {
			t.Errorf("ruta publica de mail-security inesperada: %+v", p)
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
		"ruta de un servicio de celda sin celda": func(t *routeTable) {
			cellService(t)
			t.Public = append(t.Public, publicRouteSpec{Method: "GET", Path: "/public/q/release", Service: "mail-security"})
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
		"cell_hosts_env que es la celda base": func(t *routeTable) {
			cellService(t)
			s := t.Services["mail-security"]
			s.CellHostsEnv = baseCellEnv
			t.Services["mail-security"] = s
		},
		"autenticada por el servicio sobre un servicio de celda": func(t *routeTable) {
			cellService(t)
			t.SelfAuthenticated = []selfAuthSpec{{Prefix: "inbox", Service: "mail-security"}}
		},
		"autenticada por un servicio de celda sin cell_cookie": func(t *routeTable) {
			webmailCell(t)
			t.SelfAuthenticated[0].CellCookie = ""
		},
		"autenticada por un servicio de celda sin cell_login": func(t *routeTable) {
			webmailCell(t)
			t.SelfAuthenticated[0].CellLogin = nil
		},
		"cell_login en un servicio que no es de celda": func(t *routeTable) {
			t.SelfAuthenticated = []selfAuthSpec{{Prefix: "inbox", Service: "identity",
				StrictLimit: []methodPathSpec{{Method: "POST", Path: "/session"}},
				CellLogin:   &cellLoginSpec{Method: "POST", Path: "/session", UsernameField: "username"}}}
		},
		"cell_cookie en un servicio que no es de celda": func(t *routeTable) {
			t.SelfAuthenticated = []selfAuthSpec{{Prefix: "inbox", Service: "identity", CellCookie: "cf_wm"}}
		},
		"cell_login fuera del limitador estricto": func(t *routeTable) {
			webmailCell(t)
			t.SelfAuthenticated[0].StrictLimit = []methodPathSpec{{Method: "POST", Path: "/otra"}}
		},
		"cell_login con un metodo sin cuerpo": func(t *routeTable) {
			webmailCell(t)
			t.SelfAuthenticated[0].CellLogin.Method = "GET"
			t.SelfAuthenticated[0].StrictLimit[0].Method = "GET"
		},
		"cell_login con parametro en la ruta": func(t *routeTable) {
			webmailCell(t)
			t.SelfAuthenticated[0].CellLogin.Path = "/session/{id}"
			t.SelfAuthenticated[0].StrictLimit[0].Path = "/session/{id}"
		},
		"cell_login con un campo invalido": func(t *routeTable) {
			webmailCell(t)
			t.SelfAuthenticated[0].CellLogin.UsernameField = "User-Name"
		},
		"cell_cookie invalida": func(t *routeTable) {
			webmailCell(t)
			t.SelfAuthenticated[0].CellCookie = "cf wm"
		},
		"frontend de celda": func(t *routeTable) {
			cellService(t)
			t.Frontend = "mail-security"
		},
		"servicio de celda sin organization para resolver la celda": func(t *routeTable) {
			cellService(t)
			delete(t.Services, cellDirectoryService)
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
	// Un servicio de celda sin rutas publicas: basta con sus rutas con sesion.
	tbl = base()
	cellService(&tbl)
	tbl.Public = nil
	tbl.Routes = append(tbl.Routes, routeSpec{Prefix: "mail-security", Service: "mail-security", Module: "mail_security"})
	if err := tbl.validate(); err != nil {
		t.Fatalf("un servicio de celda solo con rutas con sesion debe validar: %v", err)
	}
	// Un servicio de celda cuyo unico prefijo lo autentica el propio servicio, con su enrutado.
	tbl = base()
	webmailCell(&tbl)
	if err := tbl.validate(); err != nil {
		t.Fatalf("un servicio de celda autenticado por el servicio con cell_login y cell_cookie debe validar: %v", err)
	}
}

// webmailCell anade el webmail como servicio de celda autenticado por el servicio, con su
// enrutado por celda completo, y organization.
func webmailCell(t *routeTable) {
	t.Services["webmail"] = serviceSpec{HostEnv: "WEBMAIL_HOST", DefaultHost: "webmail", DefaultPort: "8044", CellHostsEnv: "WEBMAIL_CELL_HOSTS"}
	t.Services[cellDirectoryService] = serviceSpec{HostEnv: "ORGANIZATION_HOST", DefaultHost: "organization", DefaultPort: "8003"}
	t.SelfAuthenticated = []selfAuthSpec{{
		Prefix: "webmail", Service: "webmail",
		StrictLimit: []methodPathSpec{{Method: "POST", Path: "/session"}},
		CellLogin:   &cellLoginSpec{Method: "POST", Path: "/session", UsernameField: "username"},
		CellCookie:  "cf_wm",
	}}
}

// cellService anade un servicio de celda con una ruta por celda y organization, que resuelve
// la celda de cada empresa.
func cellService(t *routeTable) {
	t.Services["mail-security"] = serviceSpec{HostEnv: "MAIL_SECURITY_HOST", DefaultHost: "mail-security", DefaultPort: "8042", CellHostsEnv: "MAIL_SECURITY_CELL_HOSTS"}
	t.Services[cellDirectoryService] = serviceSpec{HostEnv: "ORGANIZATION_HOST", DefaultHost: "organization", DefaultPort: "8003"}
	t.Public = []publicRouteSpec{{Method: "GET", Path: "/public/q/{cell}/release", Service: "mail-security"}}
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
