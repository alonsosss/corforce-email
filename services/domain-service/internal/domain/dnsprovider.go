package domain

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// DNSProvider es un proveedor DNS en el que la plataforma puede publicar los registros de los
// dominios de una empresa con la credencial que la empresa le confia.
type DNSProvider string

const DNSProviderCloudflare DNSProvider = "cloudflare"

// DNSProviders devuelve los proveedores admitidos en orden estable.
func DNSProviders() []string { return []string{string(DNSProviderCloudflare)} }

func (p DNSProvider) Valid() bool { return p == DNSProviderCloudflare }

// DNSMode dice como se publica el DNS de un dominio: a mano por el cliente (por defecto) o por la
// plataforma en el proveedor conectado, cuyo nombre es el modo.
type DNSMode string

const DNSModeManual DNSMode = "manual"

// DNSModes devuelve los modos admitidos: manual y uno por proveedor.
func DNSModes() []string { return append([]string{string(DNSModeManual)}, DNSProviders()...) }

func (m DNSMode) Valid() bool { return m == DNSModeManual || DNSProvider(m).Valid() }

// Provider devuelve el proveedor de un modo automatico; false en modo manual.
func (m DNSMode) Provider() (DNSProvider, bool) {
	p := DNSProvider(m)
	return p, p.Valid()
}

// DNSAutomatic dice si la plataforma publica el DNS del dominio en un proveedor.
func (d *Domain) DNSAutomatic() bool {
	_, ok := d.DNSMode.Provider()
	return ok
}

const (
	minAPITokenLength = 20
	maxAPITokenLength = 256
	apiTokenHintChars = 4
	redactedToken     = "[token oculto]"
)

// Los tokens de API de los proveedores son alfanumericos con guion y guion bajo. Cualquier otro
// caracter se rechaza antes de ponerlo en una cabecera.
var apiTokenRegex = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// APIToken es la credencial de un proveedor DNS en memoria. Su valor solo sale con Reveal, que
// usa el cliente del proveedor para la cabecera Authorization y el caso de uso para cifrarlo: al
// formatearlo, serializarlo o registrarlo por accidente sale oculto.
type APIToken struct {
	value string
}

// NewAPIToken valida la forma del token sin consultar al proveedor.
func NewAPIToken(raw string) (APIToken, error) {
	v := strings.TrimSpace(raw)
	if len(v) < minAPITokenLength || len(v) > maxAPITokenLength || !apiTokenRegex.MatchString(v) {
		return APIToken{}, ErrInvalidDNSProviderToken
	}
	return APIToken{value: v}, nil
}

func (t APIToken) Reveal() string { return t.value }

// Hint son los ultimos caracteres, lo unico del token que se guarda en claro y se muestra.
func (t APIToken) Hint() string {
	if len(t.value) < apiTokenHintChars {
		return ""
	}
	return t.value[len(t.value)-apiTokenHintChars:]
}

func (t APIToken) Empty() bool { return t.value == "" }

func (APIToken) String() string               { return redactedToken }
func (APIToken) GoString() string             { return redactedToken }
func (APIToken) MarshalJSON() ([]byte, error) { return json.Marshal(redactedToken) }
func (APIToken) MarshalText() ([]byte, error) { return []byte(redactedToken), nil }

// MaxStoredZones acota los nombres de zona que se guardan con la conexion; zones_visible cuenta
// todas.
const MaxStoredZones = 500

// DNSProviderConnection es la conexion de una empresa con un proveedor DNS. TokenEnc es el token
// cifrado (ports.Cipher); nunca se serializa hacia el API.
type DNSProviderConnection struct {
	ID              uuid.UUID
	TenantID        uuid.UUID
	Provider        DNSProvider
	TokenEnc        []byte
	TokenHint       string
	Zones           []string
	ZonesVisible    int
	ConnectedBy     uuid.UUID
	ConnectedAt     time.Time
	LastValidatedAt time.Time
	UpdatedAt       time.Time
}

// SetZones guarda lo que el token ve, ordenado como lo devolvio el proveedor y acotado.
func (c *DNSProviderConnection) SetZones(zones []DNSZone, at time.Time) {
	names := make([]string, 0, min(len(zones), MaxStoredZones))
	for i, z := range zones {
		if i == MaxStoredZones {
			break
		}
		names = append(names, z.Name)
	}
	c.Zones, c.ZonesVisible, c.LastValidatedAt = names, len(zones), at
}

// DNSZone es una zona que el token del proveedor puede ver.
type DNSZone struct {
	ID   string
	Name string
}

// ZoneForDomain elige la zona del dominio entre las visibles: la de nombre igual al dominio o, si
// no la hay, la mas especifica de la que el dominio es subdominio. Nunca una zona hermana ni una
// que solo comparte sufijo de texto (otroacme.com no es zona de acme.com).
func ZoneForDomain(name string, zones []DNSZone) (DNSZone, bool) {
	name = normalizeHost(name)
	var best DNSZone
	found := false
	for _, z := range zones {
		zn := normalizeHost(z.Name)
		if zn == "" || !HostInZone(name, zn) {
			continue
		}
		if !found || len(zn) > len(normalizeHost(best.Name)) {
			best, found = z, true
		}
	}
	return best, found
}

// SameHost compara dos nombres DNS sin distinguir mayusculas ni el punto final.
func SameHost(a, b string) bool { return normalizeHost(a) == normalizeHost(b) }

// HostInZone dice si host es la zona o un nombre dentro de ella.
func HostInZone(host, zone string) bool {
	host, zone = normalizeHost(host), normalizeHost(zone)
	return zone != "" && (host == zone || strings.HasSuffix(host, "."+zone))
}

// ManagedRecordComment marca en el proveedor los registros que publico la plataforma: los que
// lleven la marca puede actualizarlos o retirarlos sola; los demas son del cliente.
const ManagedRecordComment = "cfm-managed"

// ProviderRecord es un registro tal como lo guarda el proveedor.
type ProviderRecord struct {
	ID       string
	Type     string
	Name     string
	Content  string
	Priority int
	Comment  string
}

// Managed dice si el registro lo publico la plataforma.
func (r ProviderRecord) Managed() bool {
	return strings.HasPrefix(strings.TrimSpace(r.Comment), ManagedRecordComment)
}

// DesiredRecord es un registro que el dominio necesita en su zona, en la forma del proveedor.
type DesiredRecord struct {
	Kind     RecordKind
	Type     string
	Name     string
	Content  string
	Priority int
}

// ProviderRecord es el registro que se escribe, con la marca de la plataforma.
func (r DesiredRecord) ProviderRecord(id string) ProviderRecord {
	return ProviderRecord{ID: id, Type: r.Type, Name: r.Name, Content: r.Content, Priority: r.Priority, Comment: ManagedRecordComment}
}

// DesiredRecords son los registros de ExpectedRecords en la forma del proveedor: el MX con su
// destino y su prioridad por separado.
func DesiredRecords(d *Domain, platform PlatformDNS) []DesiredRecord {
	expected := ExpectedRecords(d, platform)
	out := make([]DesiredRecord, 0, len(expected))
	for _, rec := range expected {
		dr := DesiredRecord{Kind: rec.Record, Type: rec.Type, Name: rec.Host, Content: rec.Value}
		if rec.Type == "MX" {
			dr.Content, dr.Priority = normalizeHost(platform.MXHostname), mxPriority
		}
		out = append(out, dr)
	}
	return out
}

// sameFamily dice si un registro existente ocupa el mismo sitio que el deseado: el mismo nombre y
// tipo y, en los TXT que comparten nombre con otros usos, el mismo prefijo. Un dominio solo puede
// tener un SPF y un DMARC (RFC 7208 y 7489), un TXT de propiedad y un TXT por selector DKIM, y el
// correo solo llega bien si todos sus MX son los de la plataforma.
func (r DesiredRecord) sameFamily(e ProviderRecord) bool {
	if !strings.EqualFold(e.Type, r.Type) || normalizeHost(e.Name) != normalizeHost(r.Name) {
		return false
	}
	content := strings.ToLower(normalizeTXT(e.Content))
	switch r.Kind {
	case RecordSPF:
		return content == "v=spf1" || strings.HasPrefix(content, "v=spf1 ")
	case RecordDMARC:
		return strings.HasPrefix(content, "v=dmarc1")
	case RecordOwnershipTXT:
		return strings.HasPrefix(content, ownershipTag)
	}
	return true
}

// sameContent compara lo publicado con lo deseado como lo compara un receptor: los TXT sin las
// comillas ni los trozos de 255 en que el proveedor puede devolverlos, y los MX por destino y
// prioridad.
func (r DesiredRecord) sameContent(e ProviderRecord) bool {
	if r.Type == "MX" {
		return normalizeHost(e.Content) == normalizeHost(r.Content) && e.Priority == r.Priority
	}
	return normalizeTXT(e.Content) == normalizeTXT(r.Content)
}

// txtChunk es el largo maximo de una cadena de un TXT (RFC 1035): mas largo va en varias.
const txtChunk = 255

// QuoteTXT da a un valor de TXT la forma que Cloudflare pide para su campo de contenido: entre
// comillas y, si pasa de 255 caracteres (una clave DKIM de 2048 bits), en cadenas consecutivas
// "a" "b". Sin comillas Cloudflare lo acepta, pero marca el registro con un aviso. Un valor que ya
// las lleva se deja como esta. normalizeTXT es su inversa: quita comillas y une las cadenas.
func QuoteTXT(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, `"`) {
		return value
	}
	escape := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	var parts []string
	for len(value) > txtChunk {
		parts = append(parts, `"`+escape.Replace(value[:txtChunk])+`"`)
		value = value[txtChunk:]
	}
	parts = append(parts, `"`+escape.Replace(value)+`"`)
	return strings.Join(parts, " ")
}

// quoted dice si un TXT ya esta escrito con comillas.
func quoted(content string) bool { return strings.HasPrefix(strings.TrimSpace(content), `"`) }

// normalizeTXT reduce un TXT a su valor: "a" "b" es ab, y un valor sin comillas queda igual.
func normalizeTXT(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, `"`) {
		return s
	}
	var b strings.Builder
	in, escaped := false, false
	for _, c := range s {
		switch {
		case escaped:
			b.WriteRune(c)
			escaped = false
		case in && c == '\\':
			escaped = true
		case c == '"':
			in = !in
		case in:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// RecordAction es lo que una publicacion hizo, o haria, con un registro.
type RecordAction string

const (
	RecordUnchanged RecordAction = "unchanged"
	RecordCreated   RecordAction = "created"
	RecordUpdated   RecordAction = "updated"
	// RecordReplaced: habia registros del cliente en su sitio y se reemplazaron con su confirmacion.
	RecordReplaced RecordAction = "replaced"
	// RecordConflict: hay registros del cliente en su sitio y no se tocan sin confirmacion.
	RecordConflict RecordAction = "conflict"
	// RecordFailed: el proveedor rechazo la escritura de este registro.
	RecordFailed RecordAction = "failed"
)

// RecordPlan es lo que hay que escribir en el proveedor para dejar un registro como se desea.
type RecordPlan struct {
	Desired DesiredRecord
	Action  RecordAction
	Create  bool
	// Update es el registro existente que se sobrescribe con el deseado.
	Update *ProviderRecord
	Delete []ProviderRecord
	// Foreign son los registros del cliente implicados: en conflicto o reemplazados.
	Foreign []ProviderRecord
}

// PlanRecord decide como dejar el registro deseado frente a lo que ya hay en la zona. Un registro
// identico no se toca, aunque no lleve la marca. Los registros de la plataforma que ocupan su
// sitio se actualizan o se retiran. Los del cliente solo se reemplazan con replace: sin el, el
// registro queda en conflicto y no se escribe nada.
func PlanRecord(desired DesiredRecord, existing []ProviderRecord, replace bool) RecordPlan {
	plan := RecordPlan{Desired: desired}
	var identical *ProviderRecord
	var others []ProviderRecord
	for i := range existing {
		e := existing[i]
		if !desired.sameFamily(e) {
			continue
		}
		if identical == nil && desired.sameContent(e) {
			identical = &e
			continue
		}
		others = append(others, e)
		if !e.Managed() {
			plan.Foreign = append(plan.Foreign, e)
		}
	}
	switch {
	case len(others) == 0 && identical != nil:
		// Un TXT de la plataforma publicado antes de escribirlos entre comillas vale igual para quien
		// lo consulta, pero Cloudflare lo marca con un aviso: se reescribe una vez. Uno del cliente no
		// se toca nunca, aunque coincida.
		if identical.Managed() && strings.EqualFold(desired.Type, "TXT") && !quoted(identical.Content) {
			plan.Action, plan.Update = RecordUpdated, identical
			return plan
		}
		plan.Action = RecordUnchanged
		return plan
	case len(others) == 0:
		plan.Action, plan.Create = RecordCreated, true
		return plan
	case len(plan.Foreign) > 0 && !replace:
		plan.Action = RecordConflict
		return plan
	}
	plan.Action = RecordUpdated
	if len(plan.Foreign) > 0 {
		plan.Action = RecordReplaced
	}
	if identical != nil {
		plan.Delete = others
		return plan
	}
	target := others[0]
	plan.Update = &target
	plan.Delete = others[1:]
	return plan
}

// RecordResult es el resultado de publicar un registro. Err es el fallo del proveedor si Action es
// RecordFailed; Existing son los valores del cliente en conflicto o reemplazados.
type RecordResult struct {
	Kind     RecordKind
	Type     string
	Name     string
	Content  string
	Priority int
	Action   RecordAction
	Existing []string
	Err      error
}

// DNSPublication es lo que hizo una publicacion en el proveedor.
type DNSPublication struct {
	Provider DNSProvider
	Zone     string
	Records  []RecordResult
	// Removed son los TXT de la plataforma que se retiraron (claves DKIM revocadas o fuera de
	// gracia); Kept, los del cliente en esos nombres, que no se tocan.
	Removed     []string
	Kept        []string
	PublishedAt time.Time
}

// Count cuenta los registros por accion.
func (p *DNSPublication) Count(action RecordAction) int {
	n := 0
	for _, r := range p.Records {
		if r.Action == action {
			n++
		}
	}
	return n
}

// Complete dice si todos los registros quedaron como se desean.
func (p *DNSPublication) Complete() bool {
	return p.Count(RecordConflict) == 0 && p.Count(RecordFailed) == 0
}

// ExistingValue es el valor de un registro del cliente como se muestra: el MX con su prioridad,
// como en ExpectedRecords.
func ExistingValue(r ProviderRecord) string {
	if strings.EqualFold(r.Type, "MX") {
		return normalizeHost(r.Content) + " priority " + strconv.Itoa(r.Priority)
	}
	return normalizeTXT(r.Content)
}
