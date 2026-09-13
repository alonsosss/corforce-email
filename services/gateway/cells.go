package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Rutas publicas por celda.
//
// Un servicio de celda (mail-security) se despliega una vez por celda y solo atiende a las
// empresas de la suya. Un enlace que llega por correo sin sesion lleva la celda como segmento
// {cell} de la ruta, SIN firmar: el gateway enruta por el y no verifica nada, asi que no
// recibe la clave de los enlaces. La firma del enlace incluye la celda y la comprueba el
// servicio de destino: un segmento cambiado lleva el enlace a una celda que lo rechaza.
//
// Cualquier segmento que no sea una celda declarada va al destino base del servicio (la
// celda por defecto), que lo rechaza con la misma respuesta que a una firma alterada: el
// gateway no responde nada propio y no sirve para averiguar que celdas existen.

const cellParam = "cell"

// Codigo de celda: el mismo formato que admite organization al darla de alta.
var cellCodeRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

const maxCellCodeLen = 63

// cellSegment dice si la ruta lleva {cell} como segmento entero.
func cellSegment(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		if seg == "{"+cellParam+"}" {
			return true
		}
	}
	return false
}

// validateCellHostsEnv: la variable de instancias por celda tiene forma de variable, es de
// un solo servicio y no pisa el host ni el puerto de ninguno.
func (t *routeTable) validateCellHostsEnv() error {
	taken := make(map[string]bool, 2*len(t.Services))
	for _, s := range t.Services {
		taken[s.HostEnv], taken[s.HostEnv+"_PORT"] = true, true
	}
	seen := map[string]string{}
	for name, s := range t.Services {
		if s.CellHostsEnv == "" {
			continue
		}
		if !hostEnvRe.MatchString(s.CellHostsEnv) || taken[s.CellHostsEnv] {
			return fmt.Errorf("tabla de rutas: cell_hosts_env invalido %q en %q", s.CellHostsEnv, name)
		}
		if other, dup := seen[s.CellHostsEnv]; dup {
			return fmt.Errorf("tabla de rutas: cell_hosts_env %q repetido en %q y %q", s.CellHostsEnv, other, name)
		}
		seen[s.CellHostsEnv] = name
	}
	return nil
}

// validatePublicCell: {cell} solo como segmento entero, una vez y en un servicio de celda;
// una ruta de un servicio de celda sin {cell} tiene que declarar default_cell.
func (t *routeTable) validatePublicCell(p publicRouteSpec) error {
	byCell := cellSegment(p.Path)
	if strings.Count(p.Path, "{"+cellParam) > 1 || (!byCell && strings.Contains(p.Path, "{"+cellParam)) {
		return fmt.Errorf("tabla de rutas: en %q, {%s} va una sola vez y como segmento entero", p.Path, cellParam)
	}
	cellService := t.Services[p.Service].CellHostsEnv != ""
	switch {
	case byCell && !cellService:
		return fmt.Errorf("tabla de rutas: %q enruta por celda y el servicio %q no declara cell_hosts_env", p.Path, p.Service)
	case byCell && p.DefaultCell:
		return fmt.Errorf("tabla de rutas: %q lleva {%s} y default_cell a la vez", p.Path, cellParam)
	case !byCell && cellService && !p.DefaultCell:
		return fmt.Errorf("tabla de rutas: %q es de un servicio de celda: necesita {%s} o default_cell", p.Path, cellParam)
	case !byCell && !cellService && p.DefaultCell:
		return fmt.Errorf("tabla de rutas: default_cell en %q, y %q no es un servicio de celda", p.Path, p.Service)
	}
	return nil
}

// validateCellServicesRouted: declarar cell_hosts_env sin ninguna ruta con {cell} no
// enrutaria nada por celda y dejaria una variable que nadie lee.
func (t *routeTable) validateCellServicesRouted() error {
	routed := map[string]bool{}
	for _, p := range t.Public {
		if cellSegment(p.Path) {
			routed[p.Service] = true
		}
	}
	for name, s := range t.Services {
		if s.CellHostsEnv != "" && !routed[name] {
			return fmt.Errorf("tabla de rutas: %q declara cell_hosts_env sin ninguna ruta publica con {%s}", name, cellParam)
		}
	}
	return nil
}

// loadCellTargets lee del entorno las instancias por celda de cada servicio de celda. Una
// entrada mal formada impide arrancar.
func (t *routeTable) loadCellTargets() error {
	t.cellTargets = map[string]map[string]string{}
	for name, s := range t.Services {
		if s.CellHostsEnv == "" {
			continue
		}
		targets, err := parseCellHosts(s.CellHostsEnv, os.Getenv(s.CellHostsEnv))
		if err != nil {
			return err
		}
		t.cellTargets[name] = targets
	}
	return nil
}

// cellCodes devuelve, por servicio de celda, las celdas con instancia propia, ordenadas.
func (t *routeTable) cellCodes() map[string][]string {
	out := make(map[string][]string, len(t.cellTargets))
	for name, targets := range t.cellTargets {
		codes := make([]string, 0, len(targets))
		for code := range targets {
			codes = append(codes, code)
		}
		sort.Strings(codes)
		out[name] = codes
	}
	return out
}

// parseCellHosts interpreta "celda=host:puerto" separados por comas. Vacia no declara
// ninguna instancia por celda.
func parseCellHosts(envName, raw string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	for _, entry := range strings.Split(raw, ",") {
		code, hostport, ok := strings.Cut(strings.TrimSpace(entry), "=")
		code, hostport = strings.TrimSpace(code), strings.TrimSpace(hostport)
		if !ok || len(code) > maxCellCodeLen || !cellCodeRe.MatchString(code) {
			return nil, fmt.Errorf("%s: entrada %q: se espera celda=host:puerto con el codigo de la celda", envName, entry)
		}
		if _, dup := out[code]; dup {
			return nil, fmt.Errorf("%s: celda %q repetida", envName, code)
		}
		host, port, err := net.SplitHostPort(hostport)
		n, perr := strconv.Atoi(port)
		if err != nil || host == "" || strings.ContainsAny(host, "/?#@ ") || perr != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("%s: la celda %q necesita host:puerto, llego %q", envName, code, hostport)
		}
		out[code] = "http://" + net.JoinHostPort(host, strconv.Itoa(n))
	}
	return out, nil
}

// cellRouter elige la instancia por el segmento {cell}; lo que no es una celda declarada va
// al destino base.
type cellRouter struct {
	byCell   map[string]http.Handler
	fallback http.Handler
}

func (c cellRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h, ok := c.byCell[chi.URLParam(r, cellParam)]; ok {
		h.ServeHTTP(w, r)
		return
	}
	c.fallback.ServeHTTP(w, r)
}

// mountPublic monta las rutas publicas de la tabla: las que llevan {cell}, por celda; las
// demas, al destino base de su servicio. Un proxy por destino.
func mountPublic(r chi.Router, t *routeTable, internalToken string) {
	proxies := map[string]http.Handler{}
	proxyFor := func(target string) http.Handler {
		if p, ok := proxies[target]; ok {
			return p
		}
		p := reverseProxy(target, internalToken)
		proxies[target] = p
		return p
	}
	routers := map[string]http.Handler{}
	for _, p := range t.Public {
		h := proxyFor(t.serviceURL(p.Service))
		if cellSegment(p.Path) {
			router, ok := routers[p.Service]
			if !ok {
				byCell := make(map[string]http.Handler, len(t.cellTargets[p.Service]))
				for code, target := range t.cellTargets[p.Service] {
					byCell[code] = proxyFor(target)
				}
				router = cellRouter{byCell: byCell, fallback: h}
				routers[p.Service] = router
			}
			h = router
		}
		r.Method(p.Method, p.Path, h)
	}
}
