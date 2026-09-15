package domain

import (
	"encoding/pem"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// MaxDKIMKeysPerDomain es el tope del juego de claves de un dominio: domain-service custodia la
// clave actual y, durante la gracia de una rotacion, la anterior.
const MaxDKIMKeysPerDomain = 2

// DirectoryDomain es un dominio propio del directorio de la celda (mail.v_routing_domains), no
// un dominio alias. Solo un dominio activo tiene claves DKIM en los motores.
type DirectoryDomain struct {
	Name     string
	TenantID uuid.UUID
	Active   bool
}

// DKIMKeyField es el campo de DKIM_PRIV_KEYS de un selector: selector.dominio.
func DKIMKeyField(selector, domainName string) string { return selector + "." + domainName }

// SplitDKIMKeyField separa un campo de DKIM_PRIV_KEYS. El selector no lleva puntos
// (ValidateDKIMSelector), asi que el dominio es lo que sigue al primero.
func SplitDKIMKeyField(field string) (selector, domainName string, ok bool) {
	i := strings.IndexByte(field, '.')
	if i <= 0 || i == len(field)-1 {
		return "", "", false
	}
	return field[:i], field[i+1:], true
}

// NormalizeDKIMDomain valida el dominio de una clave y lo deja en minusculas.
func NormalizeDKIMDomain(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if err := ValidateDomainName(name); err != nil {
		return "", err
	}
	return name, nil
}

// NormalizeDKIMKey valida selector y clave. El PEM debe ser una clave privada legible para no
// dejar a Rspamd sin firmar por un pegado a medias.
func NormalizeDKIMKey(k DKIMKey) (DKIMKey, error) {
	k.Selector = strings.ToLower(strings.TrimSpace(k.Selector))
	if err := ValidateDKIMSelector(k.Selector); err != nil {
		return k, err
	}
	block, _ := pem.Decode([]byte(k.PrivateKeyPEM))
	if block == nil || !strings.Contains(block.Type, "PRIVATE KEY") {
		return k, newValidation("private_key_pem no es una clave privada PEM")
	}
	return k, nil
}

// NormalizeDKIMKeySet valida el juego completo de claves de un dominio: de una a
// MaxDKIMKeysPerDomain, de selectores distintos, en el orden de publicacion (la ultima firma).
func NormalizeDKIMKeySet(domainName string, keys []DKIMKey) ([]DKIMKey, error) {
	if len(keys) == 0 || len(keys) > MaxDKIMKeysPerDomain {
		return nil, newValidation("keys debe llevar entre 1 y " + strconv.Itoa(MaxDKIMKeysPerDomain) + " claves")
	}
	out := make([]DKIMKey, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		k, err := NormalizeDKIMKey(k)
		if err != nil {
			return nil, err
		}
		if seen[k.Selector] {
			return nil, newValidation("keys repite el selector " + k.Selector)
		}
		seen[k.Selector] = true
		k.Domain = domainName
		out = append(out, k)
	}
	return out, nil
}
