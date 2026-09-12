package domain

import (
	"fmt"
	"strings"
)

// PlatformDNS son los valores de la plataforma que aparecen en los registros que el
// cliente publica. Llegan por entorno y no tienen valor por defecto en codigo: un MX o
// un include de SPF inventado enrutaria correo real a un sitio equivocado.
type PlatformDNS struct {
	// MXHostname es el host al que apunta el MX del dominio (MAIL_MX_HOSTNAME).
	MXHostname string
	// SPFInclude es el mecanismo que debe contener el SPF, p. ej. include:spf.ejemplo.com
	// (MAIL_SPF_INCLUDE).
	SPFInclude string
	// DMARCRUA es la direccion que recibe los informes agregados (MAIL_DMARC_RUA).
	DMARCRUA string
}

const (
	// OwnershipPrefix es el subdominio del TXT de propiedad y el prefijo de su valor.
	OwnershipPrefix = "_cfm-verify"
	ownershipTag    = "cfm-verify="
	mxPriority      = 10
)

// OwnershipHost es el nombre donde se publica el TXT de propiedad.
func OwnershipHost(name string) string { return OwnershipPrefix + "." + name }

// OwnershipValue es el valor del TXT de propiedad para un token.
func OwnershipValue(token string) string { return ownershipTag + token }

// DKIMHost es el nombre del TXT con la clave publica de un selector.
func DKIMHost(selector, name string) string { return selector + "._domainkey." + name }

// DKIMValue es el TXT de DKIM para una clave publica RSA en base64 DER.
func DKIMValue(publicKey string) string { return "v=DKIM1; k=rsa; p=" + publicKey }

// DMARCHost es el nombre del registro DMARC.
func DMARCHost(name string) string { return "_dmarc." + name }

// SPFValue es el TXT de SPF: solo la plataforma envia en nombre del dominio (-all).
func SPFValue(include string) string { return "v=spf1 " + include + " -all" }

// DMARCValue es el TXT de DMARC con la politica y la direccion de informes.
func DMARCValue(policy DMARCPolicy, rua string) string {
	return fmt.Sprintf("v=DMARC1; p=%s; rua=mailto:%s", policy, rua)
}

// ExpectedRecords devuelve los registros que el cliente debe publicar para el dominio.
// El MX solo se pide cuando el dominio recibe por la celda: un dominio solo de envio
// que apuntara su MX aqui perderia el correo que le llegue. DMARC se recomienda pero no
// bloquea la verificacion. Si hay una clave DKIM anterior en gracia, su TXT se incluye
// para que el cliente no lo retire antes de tiempo.
func ExpectedRecords(d *Domain, platform PlatformDNS) []DNSRecord {
	records := []DNSRecord{
		{Record: RecordOwnershipTXT, Type: "TXT", Host: OwnershipHost(d.Domain), Value: OwnershipValue(d.VerificationToken), Required: true},
	}
	if d.Purpose.IncludesCorporate() {
		records = append(records, DNSRecord{
			Record: RecordMX, Type: "MX", Host: d.Domain,
			Value: fmt.Sprintf("%s priority %d", platform.MXHostname, mxPriority), Required: true,
		})
	}
	records = append(records,
		DNSRecord{Record: RecordSPF, Type: "TXT", Host: d.Domain, Value: SPFValue(platform.SPFInclude), Required: true},
		DNSRecord{Record: RecordDKIM, Type: "TXT", Host: DKIMHost(d.DKIMSelector, d.Domain), Value: DKIMValue(d.DKIMPublicKey), Required: true},
	)
	if d.HasPreviousDKIM() {
		records = append(records, DNSRecord{
			Record: RecordDKIMPrevious, Type: "TXT", Host: DKIMHost(d.DKIMPreviousSelector, d.Domain),
			Value: DKIMValue(d.DKIMPreviousPublicKey), Required: false,
		})
	}
	records = append(records, DNSRecord{
		Record: RecordDMARC, Type: "TXT", Host: DMARCHost(d.Domain),
		Value: DMARCValue(d.DMARCPolicy, platform.DMARCRUA), Required: false,
	})
	return records
}

// normalizeHost compara nombres DNS sin distinguir mayusculas ni el punto final que
// devuelven los resolvers.
func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}
