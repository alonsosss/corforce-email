package domain

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Respuestas de un asistente a una invitacion (PARTSTAT de RFC 5545, 3.2.12, para un VEVENT).
const (
	PartStatNeedsAction = "NEEDS-ACTION"
	PartStatAccepted    = "ACCEPTED"
	PartStatTentative   = "TENTATIVE"
	PartStatDeclined    = "DECLINED"
	PartStatDelegated   = "DELEGATED"
)

var partStats = []string{PartStatNeedsAction, PartStatAccepted, PartStatTentative, PartStatDeclined, PartStatDelegated}

const (
	// MaxEventAttendees acota los invitados de un evento: cada uno es un destinatario de la invitacion.
	MaxEventAttendees = 100
	// MaxPartyNameRunes acota el nombre de un organizador o de un invitado (CN).
	MaxPartyNameRunes = 200
	maxAddressLength  = 254
)

// addressRe es una direccion de correo sin nada que pueda romper una linea de contenido ni un parametro.
var addressRe = regexp.MustCompile(`^[^\s@<>"(),;:\\\[\]]+@[^\s@<>"(),;:\\\[\]]+\.[^\s@<>"(),;:\\\[\]]+$`)

// Party es el organizador de un evento: una direccion de correo y, opcional, su nombre.
type Party struct {
	Email string
	Name  string
}

// Attendee es un invitado con su respuesta.
type Attendee struct {
	Email    string
	Name     string
	PartStat string
}

// NormalizeAddress valida una direccion de correo de un organizador o un invitado y la deja en minusculas.
func NormalizeAddress(field, raw string) (string, error) {
	a := strings.ToLower(strings.TrimSpace(raw))
	if len(a) > maxAddressLength || !addressRe.MatchString(a) {
		return "", fieldError(field, "no es una dirección de correo válida")
	}
	return a, nil
}

// NormalizePartyName valida el nombre de un organizador o de un invitado (CN): sin comillas ni controles.
func NormalizePartyName(field, raw string) (string, error) {
	name := strings.TrimSpace(strings.ReplaceAll(raw, `"`, ""))
	if err := validText(field, name, MaxPartyNameRunes, false); err != nil {
		return "", err
	}
	return name, nil
}

func normalizeParty(field string, in *Party) (*Party, error) {
	if in == nil {
		return nil, nil
	}
	email, err := NormalizeAddress(field+".email", in.Email)
	if err != nil {
		return nil, err
	}
	name, err := NormalizePartyName(field+".name", in.Name)
	if err != nil {
		return nil, err
	}
	return &Party{Email: email, Name: name}, nil
}

// NormalizePartStat admite las respuestas de un VEVENT sin distinguir mayusculas; vacio es NEEDS-ACTION.
func NormalizePartStat(field, raw string) (string, error) {
	p := strings.ToUpper(strings.TrimSpace(raw))
	if p == "" {
		return PartStatNeedsAction, nil
	}
	for _, s := range partStats {
		if s == p {
			return p, nil
		}
	}
	return "", fieldError(field, "se espera NEEDS-ACTION, ACCEPTED, TENTATIVE, DECLINED o DELEGATED")
}

func normalizeAttendees(in []Attendee) ([]Attendee, error) {
	if len(in) > MaxEventAttendees {
		return nil, fieldError("attendees", "demasiados invitados")
	}
	out := make([]Attendee, 0, len(in))
	seen := map[string]bool{}
	for i, a := range in {
		field := "attendees[" + strconv.Itoa(i) + "]"
		email, err := NormalizeAddress(field+".email", a.Email)
		if err != nil {
			return nil, err
		}
		name, err := NormalizePartyName(field+".name", a.Name)
		if err != nil {
			return nil, err
		}
		ps, err := NormalizePartStat(field+".partstat", a.PartStat)
		if err != nil {
			return nil, err
		}
		if seen[email] {
			continue
		}
		seen[email] = true
		out = append(out, Attendee{Email: email, Name: name, PartStat: ps})
	}
	return out, nil
}

// mailtoAddress lee la direccion de un valor CAL-ADDRESS (mailto:...). Lo que no es una direccion valida se
// descarta: el objeto lo escribio un tercero.
func mailtoAddress(value string) (string, bool) {
	v := strings.TrimSpace(value)
	if len(v) > 7 && strings.EqualFold(v[:7], "mailto:") {
		v = v[7:]
	}
	a, err := NormalizeAddress("address", v)
	return a, err == nil
}

// quoteParam entrecomilla el valor de un parametro que lleva separadores (RFC 5545, 3.2). Las comillas y los
// caracteres de control no pueden ir en un valor y se retiran.
func quoteParam(v string) string {
	v = strings.Map(func(r rune) rune {
		if r == '"' || r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, v)
	if strings.ContainsAny(v, ":;,") {
		return `"` + v + `"`
	}
	return v
}

func organizerLine(p Party) string {
	head := "ORGANIZER"
	if p.Name != "" {
		head += ";CN=" + quoteParam(p.Name)
	}
	return head + ":mailto:" + p.Email
}

func attendeeLine(a Attendee) string {
	head := "ATTENDEE"
	if a.Name != "" {
		head += ";CN=" + quoteParam(a.Name)
	}
	head += ";ROLE=REQ-PARTICIPANT;PARTSTAT=" + a.PartStat
	if a.PartStat == PartStatNeedsAction {
		head += ";RSVP=TRUE"
	}
	return head + ":mailto:" + a.Email
}

// withPartStat reescribe una linea ATTENDEE con otra respuesta conservando el resto de sus parametros (CN,
// ROLE, CUTYPE...). RSVP se retira: ya respondio.
func withPartStat(l rawLine, partStat string) rawLine {
	keys := make([]string, 0, len(l.params))
	for k := range l.params {
		if k != "PARTSTAT" && k != "RSVP" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	head := "ATTENDEE"
	for _, k := range keys {
		vals := make([]string, len(l.params[k]))
		for i, v := range l.params[k] {
			vals[i] = quoteParam(v)
		}
		head += ";" + k + "=" + strings.Join(vals, ",")
	}
	head += ";PARTSTAT=" + partStat
	return generated(head + ":" + l.value)
}

func (c *rawComp) organizer() *Party {
	l, ok := c.first("ORGANIZER")
	if !ok {
		return nil
	}
	email, ok := mailtoAddress(l.value)
	if !ok {
		return nil
	}
	return &Party{Email: email, Name: strings.TrimSpace(l.param("CN"))}
}

func (c *rawComp) attendees() []Attendee {
	var out []Attendee
	seen := map[string]bool{}
	for _, l := range c.props {
		if l.name != "ATTENDEE" {
			continue
		}
		email, ok := mailtoAddress(l.value)
		if !ok || seen[email] {
			continue
		}
		seen[email] = true
		ps, err := NormalizePartStat("partstat", l.param("PARTSTAT"))
		if err != nil {
			ps = PartStatNeedsAction
		}
		out = append(out, Attendee{Email: email, Name: strings.TrimSpace(l.param("CN")), PartStat: ps})
	}
	return out
}

// attendeeIndex devuelve la posicion de la linea ATTENDEE de esa direccion, o -1.
func (c *rawComp) attendeeIndex(email string) int {
	for i, l := range c.props {
		if l.name != "ATTENDEE" {
			continue
		}
		if a, ok := mailtoAddress(l.value); ok && a == email {
			return i
		}
	}
	return -1
}
