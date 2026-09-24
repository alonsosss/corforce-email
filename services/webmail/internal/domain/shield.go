package domain

import (
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
	"golang.org/x/text/unicode/norm"
)

// AuthVerdict es el resultado de SPF, DKIM o DMARC tal como lo escriben los motores de la celda.
// Vacio: el mensaje no trae resultado (un envio interno autenticado, por ejemplo).
type AuthVerdict string

const (
	AuthPass      AuthVerdict = "pass"
	AuthNone      AuthVerdict = "none"
	AuthNeutral   AuthVerdict = "neutral"
	AuthTempError AuthVerdict = "temperror"
	AuthPermError AuthVerdict = "permerror"
	AuthSoftFail  AuthVerdict = "softfail"
	AuthFail      AuthVerdict = "fail"
)

// authSeverity ordena los resultados: se queda el peor de todas las cabeceras.
var authSeverity = map[AuthVerdict]int{
	AuthPass: 1, AuthNone: 2, AuthNeutral: 3, AuthTempError: 3, AuthPermError: 4, AuthSoftFail: 5, AuthFail: 6,
}

// authAliases son los resultados de Authentication-Results (RFC 8601) que valen lo mismo que otro.
var authAliases = map[string]AuthVerdict{
	"pass": AuthPass, "none": AuthNone, "neutral": AuthNeutral, "temperror": AuthTempError,
	"permerror": AuthPermError, "softfail": AuthSoftFail, "fail": AuthFail, "hardfail": AuthFail,
	"reject": AuthFail, "quarantine": AuthFail, "policy": AuthFail,
}

// AuthResults son los tres controles del remitente.
type AuthResults struct {
	SPF   AuthVerdict
	DKIM  AuthVerdict
	DMARC AuthVerdict
}

// spamdSymbols traduce los simbolos de Rspamd de X-Spamd-Result: son los mismos con los que
// deploy/mail/rspamd/local.d/milter_headers.conf compone Authentication-Results.
var spamdSymbols = map[string]struct {
	method  string
	verdict AuthVerdict
}{
	"R_SPF_ALLOW": {"spf", AuthPass}, "R_SPF_FAIL": {"spf", AuthFail}, "R_SPF_SOFTFAIL": {"spf", AuthSoftFail},
	"R_SPF_NEUTRAL": {"spf", AuthNeutral}, "R_SPF_DNSFAIL": {"spf", AuthTempError}, "R_SPF_NA": {"spf", AuthNone},
	"R_SPF_PERMFAIL": {"spf", AuthPermError},
	"R_DKIM_ALLOW":   {"dkim", AuthPass}, "R_DKIM_REJECT": {"dkim", AuthFail}, "R_DKIM_TEMPFAIL": {"dkim", AuthTempError},
	"R_DKIM_NA": {"dkim", AuthNone}, "R_DKIM_PERMFAIL": {"dkim", AuthPermError},
	"DMARC_POLICY_ALLOW": {"dmarc", AuthPass}, "DMARC_POLICY_REJECT": {"dmarc", AuthFail},
	"DMARC_POLICY_QUARANTINE": {"dmarc", AuthFail}, "DMARC_POLICY_SOFTFAIL": {"dmarc", AuthSoftFail},
	"DMARC_NA": {"dmarc", AuthNone}, "DMARC_BAD_POLICY": {"dmarc", AuthPermError}, "DMARC_DNSFAIL": {"dmarc", AuthTempError},
}

var authCommentPattern = regexp.MustCompile(`\([^()]*\)`)

// ParseAuthentication lee Authentication-Results y X-Spamd-Result y se queda con el peor
// resultado de cada control entre todas las cabeceras. Asi una cabecera falsificada por el
// remitente, que solo puede afirmar un "pass", nunca tapa el "fail" que anoto Rspamd al recibir;
// una que afirme un fallo solo perjudica a quien la puso.
func ParseAuthentication(h MessageHeaders) AuthResults {
	var out AuthResults
	merge := func(method string, v AuthVerdict) {
		var dst *AuthVerdict
		switch method {
		case "spf":
			dst = &out.SPF
		case "dkim":
			dst = &out.DKIM
		case "dmarc":
			dst = &out.DMARC
		default:
			return
		}
		if authSeverity[v] > authSeverity[*dst] {
			*dst = v
		}
	}
	for _, header := range h[HeaderAuthResults] {
		for method, v := range parseAuthResultsHeader(header) {
			merge(method, v)
		}
	}
	for _, header := range h[HeaderSpamdResult] {
		for _, part := range strings.Split(header, ";") {
			name := strings.TrimSpace(part)
			if i := strings.IndexByte(name, '('); i >= 0 {
				name = name[:i]
			}
			if s, ok := spamdSymbols[strings.TrimSpace(name)]; ok {
				merge(s.method, s.verdict)
			}
		}
	}
	return out
}

// parseAuthResultsHeader devuelve el resultado de cada metodo de una cabecera. Varias firmas DKIM
// en la misma cabecera cuentan como su mejor resultado: basta una firma valida del dominio.
func parseAuthResultsHeader(header string) map[string]AuthVerdict {
	header = authCommentPattern.ReplaceAllString(header, " ")
	parts := strings.Split(header, ";")
	out := map[string]AuthVerdict{}
	for _, part := range parts[1:] {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		method, result, ok := strings.Cut(fields[0], "=")
		if !ok {
			continue
		}
		method = strings.ToLower(method)
		if i := strings.IndexByte(method, '/'); i >= 0 {
			method = method[:i]
		}
		v, known := authAliases[strings.ToLower(result)]
		if !known {
			continue
		}
		if prev, seen := out[method]; seen && authSeverity[prev] <= authSeverity[v] {
			continue
		}
		out[method] = v
	}
	return out
}

func (v AuthVerdict) failed() bool { return v == AuthFail }

// ShieldLevel es la gravedad del aviso. info solo marca al remitente como externo.
type ShieldLevel string

const (
	ShieldNone    ShieldLevel = "none"
	ShieldInfo    ShieldLevel = "info"
	ShieldCaution ShieldLevel = "caution"
	ShieldDanger  ShieldLevel = "danger"
)

var shieldRank = map[ShieldLevel]int{ShieldNone: 0, ShieldInfo: 1, ShieldCaution: 2, ShieldDanger: 3}

// Motivos del escudo. La interfaz explica cada uno con su texto; Params lleva los datos que lo
// concretan (el dominio parecido, el companero suplantado).
const (
	ReasonExternalSender   = "external_sender"
	ReasonSPFFail          = "spf_fail"
	ReasonDKIMFail         = "dkim_fail"
	ReasonDMARCFail        = "dmarc_fail"
	ReasonOwnDomainSpoof   = "own_domain_unverified"
	ReasonLookalikeDomain  = "lookalike_domain"
	ReasonHomoglyphDomain  = "homoglyph_domain"
	ReasonColleagueName    = "colleague_name"
	ReasonEmbeddedAddress  = "embedded_address"
	ReasonReplyToMismatch  = "reply_to_mismatch"
	paramDomain            = "domain"
	paramResembles         = "resembles"
	paramColleague         = "colleague"
	paramAddress           = "address"
	minImpersonatedNameLen = 3
)

// ShieldReason es un motivo con su gravedad.
type ShieldReason struct {
	Code   string
	Level  ShieldLevel
	Params map[string]string
}

// Shield es el veredicto del escudo antifraude sobre el remitente de un mensaje.
type Shield struct {
	Level    ShieldLevel
	External bool
	Auth     AuthResults
	Reasons  []ShieldReason
	// Partial: el directorio de la empresa no respondio y no se pudo comprobar la suplantacion de
	// un companero ni todos los dominios propios.
	Partial bool
}

// ShieldInput es lo que el escudo necesita del mensaje y de la empresa.
type ShieldInput struct {
	From    []Address
	ReplyTo []Address
	Headers MessageHeaders
	// OwnDomains son los dominios de la empresa que se conocen: el del buzon, los de sus
	// remitentes y los de los companeros del directorio.
	OwnDomains []string
	// Colleagues son buzones de la empresa cuyo nombre o direccion se parece al nombre visible del
	// remitente (la busqueda la hace el directorio).
	Colleagues []AddressBookEntry
	Partial    bool
}

// AssessSender aplica las reglas deterministas del escudo.
func AssessSender(in ShieldInput) Shield {
	out := Shield{Level: ShieldNone, Auth: ParseAuthentication(in.Headers), Partial: in.Partial}
	if len(in.From) == 0 {
		return out
	}
	sender := in.From[0]
	senderDomain := DomainOf(sender.Email)
	own := normalizeDomains(in.OwnDomains)
	internal := isOwnDomain(senderDomain, own)
	out.External = !internal
	add := func(code string, level ShieldLevel, params map[string]string) {
		out.Reasons = append(out.Reasons, ShieldReason{Code: code, Level: level, Params: params})
		if shieldRank[level] > shieldRank[out.Level] {
			out.Level = level
		}
	}

	if internal {
		if out.Auth.DMARC.failed() || out.Auth.SPF.failed() || out.Auth.DKIM.failed() {
			add(ReasonOwnDomainSpoof, ShieldDanger, map[string]string{paramDomain: senderDomain})
		}
		return out
	}
	add(ReasonExternalSender, ShieldInfo, map[string]string{paramDomain: senderDomain})

	if out.Auth.DMARC.failed() {
		add(ReasonDMARCFail, ShieldDanger, map[string]string{paramDomain: senderDomain})
	}
	if out.Auth.SPF == AuthFail || out.Auth.SPF == AuthSoftFail {
		add(ReasonSPFFail, ShieldCaution, map[string]string{paramDomain: senderDomain})
	}
	if out.Auth.DKIM.failed() {
		add(ReasonDKIMFail, ShieldCaution, map[string]string{paramDomain: senderDomain})
	}
	for _, d := range own {
		switch LookalikeKind(senderDomain, d) {
		case ReasonHomoglyphDomain:
			add(ReasonHomoglyphDomain, ShieldDanger, map[string]string{paramDomain: senderDomain, paramResembles: d})
		case ReasonLookalikeDomain:
			add(ReasonLookalikeDomain, ShieldDanger, map[string]string{paramDomain: senderDomain, paramResembles: d})
		default:
			continue
		}
		break
	}
	if c, ok := impersonatedColleague(sender, in.Colleagues); ok {
		add(ReasonColleagueName, ShieldDanger, map[string]string{paramColleague: c.DisplayName, paramAddress: c.Address})
	}
	if addr, ok := embeddedAddress(sender); ok {
		level := ShieldCaution
		if isOwnDomain(DomainOf(addr), own) {
			level = ShieldDanger
		}
		add(ReasonEmbeddedAddress, level, map[string]string{paramAddress: addr})
	}
	for _, r := range in.ReplyTo {
		if d := DomainOf(r.Email); d != "" && d != senderDomain && !isOwnDomain(d, own) {
			add(ReasonReplyToMismatch, ShieldCaution, map[string]string{paramDomain: d})
			break
		}
	}
	return out
}

// DomainOf es el dominio de una direccion en minusculas, vacio si no tiene.
func DomainOf(email string) string {
	at := strings.LastIndexByte(email, '@')
	if at < 0 || at == len(email)-1 {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(email[at+1:]), ".")
}

func normalizeDomains(list []string) []string {
	var out []string
	for _, d := range list {
		d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
		if d != "" && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// isOwnDomain: el dominio es de la empresa o un subdominio suyo.
func isOwnDomain(d string, own []string) bool {
	if d == "" {
		return false
	}
	for _, o := range own {
		if d == o || strings.HasSuffix(d, "."+o) {
			return true
		}
	}
	return false
}

// LookalikeKind compara el dominio de un remitente externo con uno propio. Devuelve
// ReasonHomoglyphDomain si solo se distinguen por caracteres que se confunden a la vista
// (cirilico por latino, "rn" por "m", "0" por "o"), ReasonLookalikeDomain si estan a una o dos
// ediciones o si el propio dominio se usa como prefijo de otro ("empresa.com.pagos.io"), y vacio si
// no se parecen.
func LookalikeKind(sender, own string) string {
	if sender == "" || own == "" || sender == own || strings.HasSuffix(sender, "."+own) {
		return ""
	}
	senderUnicode := unicodeDomain(sender)
	ownUnicode := unicodeDomain(own)
	if skeleton(senderUnicode) == skeleton(ownUnicode) {
		return ReasonHomoglyphDomain
	}
	if strings.HasPrefix(sender, own+".") {
		return ReasonLookalikeDomain
	}
	threshold := 1
	if len([]rune(ownUnicode)) >= 10 {
		threshold = 2
	}
	if editDistance(senderUnicode, ownUnicode, threshold) <= threshold ||
		editDistance(skeleton(senderUnicode), skeleton(ownUnicode), threshold) <= threshold {
		return ReasonLookalikeDomain
	}
	return ""
}

// unicodeDomain descodifica las etiquetas xn-- (IDNA) para comparar lo que ve el usuario.
func unicodeDomain(d string) string {
	if u, err := idna.Display.ToUnicode(d); err == nil {
		return strings.ToLower(u)
	}
	return d
}

// confusables son caracteres de otros alfabetos que se ven como una letra latina. No pretende ser
// la tabla completa de Unicode (UTS 39): cubre los que se usan en la practica contra dominios.
var confusables = map[rune]rune{
	'а': 'a', 'е': 'e', 'о': 'o', 'р': 'p', 'с': 'c', 'у': 'y', 'х': 'x', 'і': 'i', 'ј': 'j', 'ѕ': 's',
	'ԁ': 'd', 'ɡ': 'g', 'һ': 'h', 'ӏ': 'l', 'ո': 'n', 'ս': 'u', 'к': 'k', 'м': 'm', 'т': 't', 'в': 'b',
	'α': 'a', 'ο': 'o', 'ν': 'v', 'ρ': 'p', 'τ': 't', 'ι': 'i', 'κ': 'k', 'ε': 'e', 'υ': 'u', 'ı': 'i',
	'0': 'o', '1': 'l', 'i': 'l', '|': 'l',
}

// multiConfusables son secuencias latinas que a tamano de lectura se confunden con una letra.
var multiConfusables = strings.NewReplacer("rn", "m", "vv", "w", "cl", "d")

// skeleton reduce un texto a la forma con la que se compara a la vista: compatibilidad Unicode
// (anchos, ligaduras), sin tildes, minusculas y los confundibles llevados a su letra latina.
func skeleton(s string) string {
	s = norm.NFKD.String(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if c, ok := confusables[r]; ok {
			r = c
		}
		b.WriteRune(r)
	}
	return multiConfusables.Replace(b.String())
}

// editDistance es la distancia de Levenshtein en runas; deja de calcular al pasar de limit.
func editDistance(a, b string, limit int) int {
	ra, rb := []rune(a), []rune(b)
	if d := len(ra) - len(rb); d > limit || -d > limit {
		return limit + 1
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		best := cur[0]
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			best = min(best, cur[j])
		}
		if best > limit {
			return limit + 1
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// NormalizePersonName deja un nombre visible listo para comparar: sin tildes, minusculas, sin
// comillas y con un solo espacio entre palabras.
func NormalizePersonName(name string) string {
	name = norm.NFKD.String(strings.ToLower(name))
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.Is(unicode.Mn, r):
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// impersonatedColleague: el nombre visible de un remitente externo es el de un companero.
func impersonatedColleague(sender Address, colleagues []AddressBookEntry) (AddressBookEntry, bool) {
	name := NormalizePersonName(sender.Name)
	if len([]rune(name)) < minImpersonatedNameLen {
		return AddressBookEntry{}, false
	}
	for _, c := range colleagues {
		if strings.EqualFold(c.Address, sender.Email) {
			continue
		}
		if cn := NormalizePersonName(c.DisplayName); cn != "" && cn == name {
			return c, true
		}
		if local, _, ok := strings.Cut(strings.ToLower(c.Address), "@"); ok && NormalizePersonName(local) == name {
			return c, true
		}
	}
	return AddressBookEntry{}, false
}

var embeddedAddressPattern = regexp.MustCompile(`[^\s<>"'()]+@[^\s<>"'()]+\.[A-Za-z]{2,}`)

// embeddedAddress: el nombre visible trae una direccion distinta de la real ("ceo@empresa.com"
// <otro@gratis.net>), el disfraz mas comun en un cliente que solo muestra el nombre.
func embeddedAddress(sender Address) (string, bool) {
	found := embeddedAddressPattern.FindString(sender.Name)
	if found == "" {
		return "", false
	}
	parsed, err := mail.ParseAddress(found)
	if err != nil {
		return "", false
	}
	addr := strings.ToLower(parsed.Address)
	if addr == strings.ToLower(sender.Email) {
		return "", false
	}
	return addr, true
}
