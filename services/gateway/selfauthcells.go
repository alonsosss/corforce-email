package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/mailcell"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"go.uber.org/zap"
)

// Enrutado por celda de un prefijo que autentica el propio servicio (el webmail).
//
// El servicio no recibe empresa del gateway: su identidad es un buzon, que autentica la
// instancia de la celda del buzon contra su propio directorio. Antes del inicio de sesion la
// celda sale del dominio del nombre de usuario del cuerpo (cell_login), que organization situa en
// la celda de su empresa con el indice global de dominios activos; despues, del prefijo
// "<celda>." del token de la cookie de sesion (cell_cookie), que pone la instancia al abrirla.
// Ninguno de los dos autoriza nada: el gateway solo elige la instancia.
//
// Lo que no lleva a una celda conocida va a la celda base, que responde lo mismo que a una
// contrasena mala o a una sesion caducada: un dominio que organization no conoce, un cuerpo que
// no se entiende o que supera el tope, una cookie sin celda o con una celda sin instancia. Asi el
// gateway no responde nada propio que distinga un buzon o una celda que existen de los que no, y
// el tiempo tampoco: todo inicio de sesion con un dominio bien formado hace la misma consulta a
// organization (con la misma cache) y termina en la verificacion del buzon en alguna instancia.
// Solo cuando no se puede saber la celda de un dominio (organization sin respuesta y sin la
// ultima conocida) o su celda no tiene instancia declarada responde el gateway: 503
// CELL_UNAVAILABLE, sin salir hacia ninguna instancia ni suponer otra celda. Eso depende del
// dominio, cuyo alojamiento ya es publico por su MX, nunca del buzon.

// maxCellLoginBody es lo que el gateway lee del cuerpo del inicio de sesion para encontrar el
// nombre de usuario: el mismo tope que aplica el webmail a ese cuerpo.
const maxCellLoginBody = 8 << 10

// selfAuthHandlers devuelve el manejador de la sesion (todo el prefijo) y el del inicio de
// sesion. Sin enrutado por celda (servicio que no es de celda o despliegue de una celda) los dos
// son el destino base.
func selfAuthHandlers(t *routeTable, s selfAuthSpec, internalToken string, domains *tenantcell.Resolver, logger *zap.Logger) (session, login http.Handler) {
	base := reverseProxyWith(t.serviceURL(s.Service), internalToken, proxyServiceCSP)
	if s.CellLogin == nil || t.baseCell == "" {
		return base, base
	}
	if domains == nil {
		panic("gateway: enrutado por celda del prefijo " + s.Prefix + " sin resolvedor de dominios")
	}
	byCell := map[string]http.Handler{t.baseCell: base}
	for code, target := range t.cellTargets[s.Service] {
		byCell[code] = reverseProxyWith(target, internalToken, proxyServiceCSP)
	}
	c := &selfAuthCellRouter{
		service: s.Service, base: base, byCell: byCell, cookie: s.CellCookie,
		login: loginType(s.CellLogin.UsernameField), domains: domains, logger: logger,
	}
	routingFailuresAtZero(s.Service, routingUnresolved, routingNotServed)
	return http.HandlerFunc(c.bySession), http.HandlerFunc(c.byLogin)
}

// loginType es un struct con un unico campo de texto etiquetado con el nombre del campo del
// cuerpo. Decodificarlo con encoding/json aplica al nombre de usuario las mismas reglas que el
// servicio al leer el suyo (coincidencia sin mayusculas, la ultima clave repetida gana): gateway
// y servicio ven el mismo nombre.
func loginType(field string) reflect.Type {
	return reflect.StructOf([]reflect.StructField{{
		Name: "Username", Type: reflect.TypeOf(""), Tag: reflect.StructTag(`json:"` + field + `"`),
	}})
}

type selfAuthCellRouter struct {
	service string
	base    http.Handler
	byCell  map[string]http.Handler
	cookie  string
	login   reflect.Type
	domains *tenantcell.Resolver
	logger  *zap.Logger
}

// bySession lleva la peticion a la instancia de la celda del token de la cookie.
func (c *selfAuthCellRouter) bySession(w http.ResponseWriter, r *http.Request) {
	if cell, ok := c.sessionCell(r); ok {
		if h, served := c.byCell[cell]; served {
			h.ServeHTTP(w, r)
			return
		}
	}
	c.base.ServeHTTP(w, r)
}

// sessionCell lee la celda del prefijo "<celda>." del token de la cookie. No verifica nada: la
// instancia de esa celda autentica el token, y un prefijo cambiado solo lleva la peticion a una
// celda donde esa sesion no existe, que la rechaza y borra la cookie.
func (c *selfAuthCellRouter) sessionCell(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(c.cookie)
	if err != nil {
		return "", false
	}
	cell, _, found := strings.Cut(cookie.Value, ".")
	return cell, found && tenantcell.ValidCode(cell)
}

// byLogin lleva el inicio de sesion a la instancia de la celda del dominio del buzon.
func (c *selfAuthCellRouter) byLogin(w http.ResponseWriter, r *http.Request) {
	name, ok := c.loginDomain(r)
	if !ok {
		c.base.ServeHTTP(w, r)
		return
	}
	cell, err := c.domains.CellOf(r.Context(), name)
	switch {
	case errors.Is(err, tenantcell.ErrUnknownDomain):
		c.base.ServeHTTP(w, r)
		return
	case err != nil:
		c.refuse(w, routingUnresolved, name, "")
		response.Err(w, http.StatusServiceUnavailable, tenantcell.CodeCellUnavailable, "no se pudo determinar la celda del buzon; intentalo de nuevo")
		return
	}
	h, served := c.byCell[cell]
	if !served {
		c.refuse(w, routingNotServed, name, cell)
		response.Err(w, http.StatusServiceUnavailable, tenantcell.CodeCellUnavailable, "el servicio no esta disponible para la celda del buzon")
		return
	}
	h.ServeHTTP(w, r)
}

// loginDomain devuelve el dominio del nombre de usuario del cuerpo. Lee como mucho
// maxCellLoginBody bytes y deja el cuerpo entero para el servicio: un cuerpo mayor, que no se
// puede leer, que no se entiende o sin un nombre con dominio no tiene dominio.
func (c *selfAuthCellRouter) loginDomain(r *http.Request) (string, bool) {
	if r.Body == nil || r.Body == http.NoBody {
		return "", false
	}
	head, err := io.ReadAll(io.LimitReader(r.Body, maxCellLoginBody+1))
	if err != nil || len(head) > maxCellLoginBody {
		r.Body = prefixedBody{Reader: io.MultiReader(bytes.NewReader(head), r.Body), Closer: r.Body}
		return "", false
	}
	r.Body = io.NopCloser(bytes.NewReader(head))
	r.ContentLength = int64(len(head))
	r.TransferEncoding = nil
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(head)), nil }

	dst := reflect.New(c.login)
	if json.NewDecoder(bytes.NewReader(head)).Decode(dst.Interface()) != nil {
		return "", false
	}
	return mailDomainOf(dst.Elem().Field(0).String())
}

// mailDomainOf devuelve el dominio de un nombre de buzon con la normalizacion del webmail
// (minusculas, sin espacios alrededor) y la regla de dominio del indice (mailcell).
func mailDomainOf(username string) (string, bool) {
	local, domainPart, found := strings.Cut(strings.ToLower(strings.TrimSpace(username)), "@")
	if !found || local == "" {
		return "", false
	}
	return mailcell.NormalizeDomain(domainPart)
}

// prefixedBody devuelve al servicio lo ya leido seguido del resto del cuerpo original.
type prefixedBody struct {
	io.Reader
	io.Closer
}

func (c *selfAuthCellRouter) refuse(w http.ResponseWriter, reason, domain, cell string) {
	cellRoutingFailures.WithLabelValues(c.service, reason).Inc()
	w.Header().Set("Cache-Control", "no-store")
	c.logger.Warn("celdas: inicio de sesion no enviado a ninguna celda",
		zap.String("service", c.service), zap.String("reason", reason),
		zap.String("domain", domain), zap.String("cell", cell))
}
