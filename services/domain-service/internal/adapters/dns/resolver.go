package dns

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
)

// lookupTimeout acota cada consulta: una zona que no responde no debe colgar la
// verificacion ni el barrido.
const lookupTimeout = 5 * time.Second

// Resolver implementa ports.DNSResolver sobre net.Resolver. Un nombre sin registros
// devuelve lista vacia sin error; solo un fallo de consulta (timeout, servidor caido)
// devuelve error, y es lo que Evaluate trata como resultado no concluyente.
type Resolver struct {
	resolver *net.Resolver
}

// New construye el resolver. server es un host:port opcional (MAIL_DNS_RESOLVER); vacio
// usa el del sistema. Con servidor propio se fuerza el resolver puro de Go, que es el
// que respeta el Dial.
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

func (r *Resolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	txt, err := r.resolver.LookupTXT(ctx, name)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return txt, nil
}

func (r *Resolver) LookupMX(ctx context.Context, name string) ([]domain.MXRecord, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	mx, err := r.resolver.LookupMX(ctx, name)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]domain.MXRecord, 0, len(mx))
	for _, m := range mx {
		out = append(out, domain.MXRecord{Host: m.Host, Priority: m.Pref})
	}
	return out, nil
}

// isNotFound distingue "no hay registro" (NXDOMAIN o respuesta vacia) de un fallo de
// consulta. Go marca ambos como DNSError, pero solo el primero lleva IsNotFound.
func isNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}
