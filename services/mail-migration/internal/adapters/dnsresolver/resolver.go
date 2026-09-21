// Package dnsresolver resuelve el servidor de origen de una migracion con el resolvedor del sistema.
package dnsresolver

import (
	"context"
	"net"
	"net/netip"
	"time"
)

const lookupTimeout = 5 * time.Second

type Resolver struct {
	resolver *net.Resolver
}

func New() *Resolver { return &Resolver{resolver: net.DefaultResolver} }

// LookupAddrs devuelve todas las direcciones IPv4 e IPv6 del nombre, o la propia direccion si es un
// literal. El llamante decide si son publicas.
func (r *Resolver) LookupAddrs(ctx context.Context, host string) ([]netip.Addr, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{addr}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	found, err := r.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	return found, nil
}
