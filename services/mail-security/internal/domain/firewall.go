package domain

import (
	"math"
	"net/netip"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// FirewallList es la lista del cortafuegos de la celda a la que pertenece una red.
type FirewallList string

const (
	FirewallAllow FirewallList = "allow"
	FirewallDeny  FirewallList = "deny"
)

// RedisKey es el hash de netfilter que corresponde a la lista.
func (l FirewallList) RedisKey() string {
	if l == FirewallDeny {
		return RedisF2BBlacklist
	}
	return RedisF2BWhitelist
}

// FirewallNetwork es una red de las listas del cortafuegos. Es de la PLATAFORMA: el
// cortafuegos protege a toda la celda, no a una empresa.
type FirewallNetwork struct {
	ID        uuid.UUID    `json:"id"`
	List      FirewallList `json:"list"`
	Network   string       `json:"network"`
	Note      string       `json:"note"`
	CreatedAt time.Time    `json:"created_at"`
}

// NormalizeFirewallNetwork acepta una IP o un CIDR y devuelve la red en forma canonica
// (siempre con prefijo), que es como netfilter la lee de sus listas.
func NormalizeFirewallNetwork(raw string) (string, error) {
	p, err := parsePrefix(raw)
	if err != nil {
		return "", newValidation("network debe ser una IP o un CIDR")
	}
	return p.String(), nil
}

// ValidateFirewallList exige una de las dos listas.
func ValidateFirewallList(l FirewallList) error {
	if l != FirewallAllow && l != FirewallDeny {
		return newValidation("list debe ser allow o deny")
	}
	return nil
}

// FirewallOptions son las opciones de baneo que netfilter lee de F2B_OPTIONS. Tiempos en
// segundos; netban_* es el prefijo con el que se banea la red de la IP infractora.
type FirewallOptions struct {
	BanTime          int       `json:"ban_time"`
	MaxBanTime       int       `json:"max_ban_time"`
	BanTimeIncrement bool      `json:"ban_time_increment"`
	MaxAttempts      int       `json:"max_attempts"`
	RetryWindow      int       `json:"retry_window"`
	NetbanIPv4       int       `json:"netban_ipv4"`
	NetbanIPv6       int       `json:"netban_ipv6"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// DefaultFirewallOptions son los valores con los que netfilter arranca sin F2B_OPTIONS
// (deploy/mail/netfilter/main.py, verifyF2boptions).
func DefaultFirewallOptions() FirewallOptions {
	return FirewallOptions{BanTime: 1800, MaxBanTime: 10000, BanTimeIncrement: true, MaxAttempts: 10,
		RetryWindow: 600, NetbanIPv4: 32, NetbanIPv6: 128}
}

func (o FirewallOptions) Validate() error {
	switch {
	case o.BanTime <= 0:
		return newValidation("ban_time debe ser mayor que cero")
	case o.MaxBanTime < o.BanTime:
		return newValidation("max_ban_time no puede ser menor que ban_time")
	case o.MaxAttempts <= 0:
		return newValidation("max_attempts debe ser mayor que cero")
	case o.RetryWindow <= 0:
		return newValidation("retry_window debe ser mayor que cero")
	case o.NetbanIPv4 < 8 || o.NetbanIPv4 > 32:
		return newValidation("netban_ipv4 debe estar entre 8 y 32")
	case o.NetbanIPv6 < 8 || o.NetbanIPv6 > 128:
		return newValidation("netban_ipv6 debe estar entre 8 y 128")
	}
	return nil
}

// Managed son las claves de F2B_OPTIONS que gobierna la plataforma. El resto
// (banlist_id, manage_external) las mantiene netfilter y se conservan al escribir.
func (o FirewallOptions) Managed() map[string]interface{} {
	return map[string]interface{}{
		"ban_time": o.BanTime, "max_ban_time": o.MaxBanTime, "ban_time_increment": o.BanTimeIncrement,
		"max_attempts": o.MaxAttempts, "retry_window": o.RetryWindow,
		"netban_ipv4": o.NetbanIPv4, "netban_ipv6": o.NetbanIPv6,
	}
}

// FirewallBan es un baneo vigente de netfilter: temporal (con vencimiento) o permanente
// (una red de la lista de denegadas).
type FirewallBan struct {
	Network   string     `json:"network"`
	Permanent bool       `json:"permanent"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// ParseFirewallBans traduce F2B_ACTIVE_BANS (red -> vencimiento en segundos unix) y
// F2B_PERM_BANS (red -> alta) a la lista de baneos, ordenada por red. Un vencimiento que
// no es un numero finito se descarta aqui: NaN o Inf no se pueden serializar en JSON.
func ParseFirewallBans(active, permanent map[string]string) []FirewallBan {
	out := make([]FirewallBan, 0, len(active)+len(permanent))
	for network, raw := range active {
		ban := FirewallBan{Network: network}
		if secs, err := strconv.ParseFloat(raw, 64); err == nil && !math.IsNaN(secs) && !math.IsInf(secs, 0) && secs > 0 {
			t := time.Unix(int64(secs), 0).UTC()
			ban.ExpiresAt = &t
		}
		out = append(out, ban)
	}
	for network := range permanent {
		out = append(out, FirewallBan{Network: network, Permanent: true})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Network == out[j].Network {
			return !out[i].Permanent
		}
		return out[i].Network < out[j].Network
	})
	return out
}

// NormalizeBannedNetwork valida la red de un desbaneo con la forma en que netfilter la
// guarda (siempre con prefijo).
func NormalizeBannedNetwork(raw string) (string, error) {
	p, err := netip.ParsePrefix(raw)
	if err != nil {
		return "", newValidation("network debe ser una red con prefijo, como aparece en los baneos")
	}
	return p.Masked().String(), nil
}
