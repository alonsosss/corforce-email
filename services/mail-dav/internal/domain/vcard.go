package domain

import (
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Property es una linea de contenido de un vCard, ya desdoblada. Name va en mayusculas y sin grupo.
type Property struct {
	Group string
	Name  string
	Value string
}

// Card es un vCard validado: lo que hace falta para indexarlo y filtrarlo. El texto original no se
// toca: se guarda y se devuelve tal como lo envio el cliente.
type Card struct {
	Version string
	UID     string
	Props   []Property
}

const (
	maxUIDLength   = 255
	maxEmails      = 50
	maxEmailLength = 320
)

var propertyNameRe = regexp.MustCompile(`^(?:[A-Za-z0-9-]+\.)?[A-Za-z0-9-]+$`)

func vcardError(reason string) *VCardError { return &VCardError{Reason: reason} }

// ParseVCard valida un vCard 3.0 o 4.0 que llega de un tercero: acotado en bytes y propiedades, UTF-8
// sin caracteres de control, una sola tarjeta sin anidar, con VERSION y UID. No interpreta parametros
// ni valores: solo la estructura que hace falta para que lo guardado sea siempre una tarjeta completa.
func ParseVCard(raw string, lim Limits) (Card, error) {
	if len(raw) > lim.MaxVCardBytes {
		return Card{}, &VCardError{Reason: "el vCard supera el tamano maximo", TooLarge: true}
	}
	if !utf8.ValidString(raw) {
		return Card{}, vcardError("no es UTF-8 valido")
	}
	if err := rejectControl(raw); err != nil {
		return Card{}, err
	}
	lines := unfold(raw)
	if len(lines) < 2 || !strings.EqualFold(lines[0], "BEGIN:VCARD") || !strings.EqualFold(lines[len(lines)-1], "END:VCARD") {
		return Card{}, vcardError("debe empezar con BEGIN:VCARD y terminar con END:VCARD")
	}
	body := lines[1 : len(lines)-1]
	if len(body) > lim.MaxVCardProperties {
		return Card{}, vcardError("demasiadas propiedades")
	}
	card := Card{Props: make([]Property, 0, len(body))}
	for _, line := range body {
		p, err := parseLine(line)
		if err != nil {
			return Card{}, err
		}
		switch p.Name {
		case "BEGIN", "END":
			return Card{}, vcardError("un vCard no puede contener otro")
		case "VERSION":
			if card.Version != "" {
				return Card{}, vcardError("VERSION repetida")
			}
			card.Version = p.Value
		case "UID":
			if card.UID != "" {
				return Card{}, vcardError("UID repetido")
			}
			card.UID = strings.TrimSpace(p.Value)
		}
		card.Props = append(card.Props, p)
	}
	if card.Version != "3.0" && card.Version != "4.0" {
		return Card{}, vcardError("solo se admiten las versiones 3.0 y 4.0")
	}
	if card.UID == "" || len(card.UID) > maxUIDLength {
		return Card{}, vcardError("el vCard debe traer un UID de hasta 255 caracteres")
	}
	return card, nil
}

// ParseStoredVCard vuelve a leer un vCard ya guardado. No aplica los topes de hoy: uno aceptado bajo
// limites mas holgados sigue siendo una tarjeta valida.
func ParseStoredVCard(raw string) (Card, error) {
	return ParseVCard(raw, Limits{MaxVCardBytes: len(raw), MaxVCardProperties: math.MaxInt})
}

// rejectControl aplica controlProblem a un vCard.
func rejectControl(raw string) error {
	if reason := controlProblem(raw); reason != "" {
		return vcardError(reason)
	}
	return nil
}

// controlProblem deja pasar solo tabulador, salto de linea y el retorno de carro que lo acompana: un
// CR suelto o un NUL hacen que dos lectores vean lineas distintas. Devuelve la razon del rechazo o "".
func controlProblem(raw string) string {
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '\n' || c == '\t':
		case c == '\r':
			if i+1 >= len(raw) || raw[i+1] != '\n' {
				return "contiene un retorno de carro suelto"
			}
		case c < 0x20 || c == 0x7f:
			return "contiene caracteres de control"
		}
	}
	return ""
}

// unfold parte en lineas (CRLF o LF), une las continuaciones (RFC 6350, 3.2) y descarta las vacias. Cada
// linea logica se une una sola vez: concatenar cada continuacion sobre la linea acumulada es cuadratico, y
// el cuerpo de un PUT (o cada objeto de una consulta) es entrada de un tercero.
func unfold(raw string) []string {
	var out, pieces []string
	flush := func() {
		switch len(pieces) {
		case 0:
		case 1:
			out = append(out, pieces[0])
		default:
			out = append(out, strings.Join(pieces, ""))
		}
		pieces = pieces[:0]
	}
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if line != "" && (line[0] == ' ' || line[0] == '\t') && len(pieces) > 0 {
			pieces = append(pieces, line[1:])
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		flush()
		pieces = append(pieces, line)
	}
	flush()
	return out
}

// splitContentLine separa "cabecera:valor" de una linea de contenido (vCard o iCalendar). Los dos puntos
// dentro de un parametro entre comillas no cierran la cabecera.
func splitContentLine(line string) (head, value string, ok bool) {
	inQuote := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			inQuote = !inQuote
		case ':':
			if !inQuote {
				return line[:i], line[i+1:], i >= 1
			}
		}
	}
	return "", "", false
}

// parseLine separa "grupo.NOMBRE;parametros:valor".
func parseLine(line string) (Property, error) {
	head, value, ok := splitContentLine(line)
	if !ok {
		return Property{}, vcardError("linea sin nombre o sin valor")
	}
	name, _, _ := strings.Cut(head, ";")
	if !propertyNameRe.MatchString(name) {
		return Property{}, vcardError("nombre de propiedad no valido")
	}
	group, bare := "", name
	if g, n, ok := strings.Cut(name, "."); ok {
		group, bare = g, n
	}
	return Property{Group: group, Name: strings.ToUpper(bare), Value: unescapeText(value)}, nil
}

// unescapeText deshace los escapes de texto de vCard (\n, \, \; y \\).
func unescapeText(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			if s[i] == 'n' || s[i] == 'N' {
				b.WriteByte('\n')
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// First devuelve el valor de la primera propiedad con ese nombre.
func (c Card) First(name string) (string, bool) {
	for _, p := range c.Props {
		if p.Name == name {
			return p.Value, true
		}
	}
	return "", false
}

// Emails devuelve las direcciones EMAIL, sin repetir, con un tope.
func (c Card) Emails() []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range c.Props {
		v := strings.TrimSpace(p.Value)
		if p.Name != "EMAIL" || v == "" || len(v) > maxEmailLength || seen[strings.ToLower(v)] {
			continue
		}
		seen[strings.ToLower(v)] = true
		if out = append(out, v); len(out) == maxEmails {
			break
		}
	}
	return out
}

// DisplayName es el nombre con el que se ve el contacto: FN, o si falta N, ORG, el primer correo y
// por ultimo el UID.
func (c Card) DisplayName() string {
	if v, ok := c.First("FN"); ok && strings.TrimSpace(v) != "" {
		return truncateRunes(strings.TrimSpace(v), MaxContactDisplayRunes)
	}
	if v, ok := c.First("N"); ok {
		if name := strings.TrimSpace(strings.ReplaceAll(v, ";", " ")); name != "" {
			return truncateRunes(strings.Join(strings.Fields(name), " "), MaxContactDisplayRunes)
		}
	}
	if v, ok := c.First("ORG"); ok && strings.TrimSpace(v) != "" {
		return truncateRunes(strings.TrimSpace(strings.ReplaceAll(v, ";", " ")), MaxContactDisplayRunes)
	}
	if emails := c.Emails(); len(emails) > 0 {
		return truncateRunes(emails[0], MaxContactDisplayRunes)
	}
	return truncateRunes(c.UID, MaxContactDisplayRunes)
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// NewContact valida el vCard y arma el contacto con su etag y sus campos indexados. Los
// identificadores y las fechas los pone quien lo guarda.
func NewContact(resourceName, raw string, lim Limits) (Contact, error) {
	if !ValidResourceName(resourceName) {
		return Contact{}, ErrInvalidName
	}
	card, err := ParseVCard(raw, lim)
	if err != nil {
		return Contact{}, err
	}
	return Contact{
		ResourceName: resourceName,
		UID:          card.UID,
		VCard:        raw,
		Size:         len(raw),
		ETag:         ETagOf(raw),
		DisplayName:  card.DisplayName(),
		Emails:       card.Emails(),
	}, nil
}
