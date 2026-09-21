package main

import (
	"net"
	"net/http"
	"strings"
)

// forwardedScheme decide el esquema que el gateway declara a los servicios en
// X-Forwarded-Proto. Una conexion TLS del propio gateway es https. Si no, vale lo que el
// borde declare, pero solo "http" o "https" (sin distinguir mayusculas; en una lista, el
// primero, el esquema original del visitante): cualquier otro texto se descarta, porque la
// cabecera la puede escribir quien llegue al gateway y se copia a los servicios que forman
// enlaces con ella. Sin una declaracion valida se supone https, salvo que el Host sea el de
// una maquina local, que es el desarrollo sin proxy.
func forwardedScheme(req *http.Request) string {
	if req.TLS != nil {
		return "https"
	}
	first, _, _ := strings.Cut(req.Header.Get("X-Forwarded-Proto"), ",")
	switch scheme := strings.ToLower(strings.TrimSpace(first)); scheme {
	case "http", "https":
		return scheme
	}
	if isLocalHost(req.Host) {
		return "http"
	}
	return "https"
}

// isLocalHost dice si el Host de la peticion nombra la propia maquina: localhost o una
// direccion de loopback, con o sin puerto. Un nombre que solo empieza por "localhost" no lo es.
func isLocalHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
