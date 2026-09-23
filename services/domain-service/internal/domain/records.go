package domain

import (
	"fmt"
	"regexp"
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
	// TLSRPTRUA es la direccion que recibe los informes de fallos de TLS (MAIL_TLSRPT_RUA). Es
	// opcional: sin ella no se pide el registro _smtp._tls.
	TLSRPTRUA string
	// SESRegion es la region de Amazon SES (SES_REGION) cuando domain-service da de alta los
	// dominios de envio en SES. Vacia si esa integracion esta desactivada: sin ella no se piden los
	// registros del MAIL FROM.
	SESRegion string
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

// ValidateReportAddress comprueba la direccion que va tras mailto: en la etiqueta rua de un TXT: una
// sola direccion, sin espacios ni los separadores del registro (coma y punto y coma), porque una
// direccion partida cambiaria a donde se envian los informes de todas las empresas.
func ValidateReportAddress(addr string) error {
	local, host, ok := strings.Cut(addr, "@")
	if !ok || local == "" || host == "" || strings.ContainsAny(addr, " \t\r\n,;!") || strings.Count(addr, "@") != 1 {
		return ErrInvalidReportAddress
	}
	if validateDNSName(NormalizeDomainName(host)) != nil {
		return ErrInvalidReportAddress
	}
	return nil
}

// MTASTSHost es el nombre del TXT que anuncia la politica MTA-STS.
func MTASTSHost(name string) string { return "_mta-sts." + name }

// MTASTSValue es el TXT de MTA-STS: el id cambia con cada version de la politica (RFC 8461, 3.1).
func MTASTSValue(policyID string) string { return "v=STSv1; id=" + policyID }

// TLSRPTHost es el nombre del TXT de TLS-RPT.
func TLSRPTHost(name string) string { return "_smtp._tls." + name }

// TLSRPTValue es el TXT de TLS-RPT con la direccion de informes (RFC 8460, 3).
func TLSRPTValue(rua string) string { return "v=TLSRPTv1; rua=mailto:" + rua }

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
// para que el cliente no lo retire antes de tiempo. En un dominio que recibe por la celda, mtaSTSPolicyID
// (la version de su politica MTA-STS, vacia si no la publica) pide el TXT _mta-sts, y TLSRPTRUA, si esta
// configurada, el TXT _smtp._tls; ninguno es requerido. Un dominio que envia por SES, con la integracion
// activa (platform.SESRegion), pide el MX y el SPF de su subdominio MAIL FROM (bounce.<dominio>), tambien
// recomendados.
func ExpectedRecords(d *Domain, platform PlatformDNS, mtaSTSPolicyID string) []DNSRecord {
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
	if d.Purpose.IncludesSending() && platform.SESRegion != "" {
		mailFrom := SESMailFromDomain(d.Domain)
		records = append(records,
			DNSRecord{
				Record: RecordSESMailFromMX, Type: "MX", Host: mailFrom,
				Value: fmt.Sprintf("%s priority %d", SESFeedbackHost(platform.SESRegion), mxPriority), Required: false,
			},
			DNSRecord{Record: RecordSESMailFromSPF, Type: "TXT", Host: mailFrom, Value: SESMailFromSPFValue, Required: false},
		)
	}
	if d.Purpose.IncludesCorporate() {
		if mtaSTSPolicyID != "" {
			records = append(records, DNSRecord{
				Record: RecordMTASTS, Type: "TXT", Host: MTASTSHost(d.Domain), Value: MTASTSValue(mtaSTSPolicyID), Required: false,
			})
		}
		if platform.TLSRPTRUA != "" {
			records = append(records, DNSRecord{
				Record: RecordTLSRPT, Type: "TXT", Host: TLSRPTHost(d.Domain), Value: TLSRPTValue(platform.TLSRPTRUA), Required: false,
			})
		}
	}
	return records
}

// sesRegionPattern es la forma de una region de AWS (us-east-1, eu-central-1): va dentro de un MX.
var sesRegionPattern = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)

// ValidSESRegion dice si region tiene la forma de una region de AWS.
func ValidSESRegion(region string) bool { return sesRegionPattern.MatchString(region) }

// normalizeHost compara nombres DNS sin distinguir mayusculas ni el punto final que
// devuelven los resolvers.
func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}
