// Package dns consulta los registros de un dominio que el directorio comprueba antes de aceptar un
// cambio: hoy, los MX que exige una politica MTA-STS en enforce.
package dns

import (
	"context"
	"errors"
	"net"
	"time"
)

// lookupTimeout acota cada consulta: una zona que no responde no debe colgar la peticion.
const lookupTimeout = 5 * time.Second

// Resolver implementa ports.MXResolver sobre net.Resolver.
type Resolver struct {
	resolver *net.Resolver
}

// New construye el resolver. server es un host:puerto opcional (MAIL_DNS_RESOLVER, el mismo con el
// que domain-service verifica los dominios); vacio usa el del sistema. Con servidor propio se fuerza
// el resolver puro de Go, que es el que respeta el Dial.
func New(server string) *Resolver {
	if server == "" {
		return &Resolver{resolver: net.DefaultResolver}
	}
	dialer := &net.Dialer{Timeout: lookupTimeout}
	return &Resolver{resolver: &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, server)
		},
	}}
}

// LookupMX devuelve los nombres de los MX del dominio. Sin registros, lista vacia y sin error: solo un
// fallo de la consulta (tiempo agotado, servidor caido) devuelve error.
func (r *Resolver) LookupMX(ctx context.Context, name string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	mx, err := r.resolver.LookupMX(ctx, name)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return nil, nil
		}
		return nil, err
	}
	hosts := make([]string, 0, len(mx))
	for _, m := range mx {
		hosts = append(hosts, m.Host)
	}
	return hosts, nil
}
