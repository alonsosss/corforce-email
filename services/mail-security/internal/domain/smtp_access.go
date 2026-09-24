package domain

import (
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MaxSMTPAccessNetworks acota las redes por buzon: cada una es un campo del hash que
// Rspamd consulta en cada envio autenticado del buzon.
const MaxSMTPAccessNetworks = 64

// Prefijos que SMTP_ACCESS de Rspamd sabe comparar (rspamd.local.lua recorre de /32 a /8
// en IPv4 y de /128 a /32 en IPv6).
const (
	minSMTPPrefixV4 = 8
	minSMTPPrefixV6 = 32
)

// SMTPAccess restringe desde que redes puede enviar un buzon por SMTP autenticado. Sin
// redes el buzon envia desde cualquier sitio. Networks va en la forma de SMTPNetworkField.
type SMTPAccess struct {
	TenantID  uuid.UUID `json:"tenant_id"`
	Username  string    `json:"username"`
	Networks  []string  `json:"networks"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NormalizeSMTPNetwork acepta una IP o un CIDR y devuelve el prefijo enmascarado. Una red
// mas ancha que lo que SMTP_ACCESS compara no casaria nunca y dejaria al buzon sin poder
// enviar: se rechaza.
func NormalizeSMTPNetwork(raw string) (netip.Prefix, error) {
	p, err := parsePrefix(raw)
	if err != nil {
		return netip.Prefix{}, newValidation(fmt.Sprintf("%q no es una IP ni un CIDR", strings.TrimSpace(raw)))
	}
	min := minSMTPPrefixV4
	if p.Addr().Is6() {
		min = minSMTPPrefixV6
	}
	if p.Bits() < min {
		return netip.Prefix{}, newValidation(fmt.Sprintf("%s es demasiado ancha: el mínimo es /%d", p, min))
	}
	return p, nil
}

// NormalizeSMTPNetworks normaliza, quita duplicados y acota la lista de redes de un buzon.
func NormalizeSMTPNetworks(raw []string) ([]netip.Prefix, error) {
	seen := make(map[netip.Prefix]bool, len(raw))
	out := make([]netip.Prefix, 0, len(raw))
	for _, r := range raw {
		p, err := NormalizeSMTPNetwork(r)
		if err != nil {
			return nil, err
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, newValidation("networks no puede ir vacío; para quitar la restricción borra la entrada")
	}
	if len(out) > MaxSMTPAccessNetworks {
		return nil, newValidation(fmt.Sprintf("networks admite como mucho %d redes", MaxSMTPAccessNetworks))
	}
	return out, nil
}

// SMTPNetworkField es como Rspamd busca la red en SMTP_ALLOW_NETS_<usuario>: la IP sola
// para un host y red/prefijo para el resto (HMGET ip, ip/32, ip/31, ...).
func SMTPNetworkField(p netip.Prefix) string {
	if p.IsSingleIP() {
		return p.Addr().String()
	}
	return p.String()
}

// parsePrefix admite IP o CIDR, sin zona, y deja las IPv4 mapeadas en IPv6 como IPv4: el
// motor ve la conexion IPv4 como IPv4.
func parsePrefix(raw string) (netip.Prefix, error) {
	s := strings.TrimSpace(raw)
	if addr, err := netip.ParseAddr(s); err == nil {
		if addr.Zone() != "" {
			return netip.Prefix{}, fmt.Errorf("zona no admitida")
		}
		addr = addr.Unmap()
		return netip.PrefixFrom(addr, addr.BitLen()), nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	if p.Addr().Is4In6() {
		bits := p.Bits() - 96
		if bits < 0 {
			return netip.Prefix{}, fmt.Errorf("prefijo mapeado demasiado ancho")
		}
		p = netip.PrefixFrom(p.Addr().Unmap(), bits)
	}
	return p.Masked(), nil
}
