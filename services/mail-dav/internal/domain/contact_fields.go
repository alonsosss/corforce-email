package domain

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Tipos de un correo o un telefono en la API estructurada.
const (
	ContactTypeHome   = "home"
	ContactTypeWork   = "work"
	ContactTypeMobile = "mobile"
	ContactTypeOther  = "other"
)

const (
	// MaxContactTextRunes acota cada campo de texto de una linea de un contacto (nombre, empresa, cargo).
	MaxContactTextRunes = MaxContactDisplayRunes
	// MaxContactNotesRunes acota las notas de un contacto.
	MaxContactNotesRunes = 10000
	// MaxContactEmails y MaxContactPhones acotan cuantos correos y telefonos lleva un contacto.
	MaxContactEmails = maxEmails
	MaxContactPhones = 50
	maxPhoneRunes    = 64
)

const (
	vcard3 = "3.0"
	vcard4 = "4.0"
	// layoutStamp es una fecha y hora en UTC (REV, DTSTAMP, LAST-MODIFIED).
	layoutStamp = "20060102T150405Z"
)

// TypedValue es un correo o un telefono con su tipo (home, work, mobile u other).
type TypedValue struct {
	Value string
	Type  string
}

// ContactFields es un contacto en la forma de la API estructurada. Birthday es "AAAA-MM-DD" o "--MM-DD"
// (sin anio), o vacio.
type ContactFields struct {
	Name         string
	GivenName    string
	FamilyName   string
	Emails       []TypedValue
	Phones       []TypedValue
	Organization string
	Title        string
	Notes        string
	Birthday     string
}

func validText(field, v string, maxRunes int, multiline bool) error {
	if !utf8.ValidString(v) {
		return fieldError(field, "no es UTF-8 válido")
	}
	if hasControl(v, multiline) {
		return fieldError(field, "contiene caracteres de control")
	}
	if utf8.RuneCountInString(v) > maxRunes {
		return fieldError(field, "supera el largo máximo")
	}
	return nil
}

func normalizeType(field, t string) (string, error) {
	switch t = strings.ToLower(strings.TrimSpace(t)); t {
	case "":
		return ContactTypeOther, nil
	case ContactTypeHome, ContactTypeWork, ContactTypeMobile, ContactTypeOther:
		return t, nil
	}
	return "", fieldError(field, "tipo no admitido: home, work, mobile u other")
}

// validEmail pide una direccion con parte local y dominio, sin espacios ni separadores de vCard.
func validEmail(v string) bool {
	local, host, ok := strings.Cut(v, "@")
	return ok && local != "" && host != "" && !strings.Contains(host, "@") && len(v) <= maxEmailLength && utf8.ValidString(v) &&
		!strings.ContainsAny(v, " \t<>,;\"\\")
}

// Normalize valida el contacto y lo deja listo para escribirlo: textos sin espacios en los extremos, tipos
// en minusculas y notas con saltos LF. Un contacto sin nombre, empresa, correo ni telefono no se admite.
func (f ContactFields) Normalize() (ContactFields, error) {
	out := ContactFields{
		Name: strings.TrimSpace(f.Name), GivenName: strings.TrimSpace(f.GivenName), FamilyName: strings.TrimSpace(f.FamilyName),
		Organization: strings.TrimSpace(f.Organization), Title: strings.TrimSpace(f.Title),
		Notes:    strings.TrimSpace(strings.ReplaceAll(f.Notes, "\r\n", "\n")),
		Birthday: strings.TrimSpace(f.Birthday),
	}
	for _, t := range []struct {
		field, value string
	}{{"name", out.Name}, {"given_name", out.GivenName}, {"family_name", out.FamilyName}, {"organization", out.Organization}, {"title", out.Title}} {
		if err := validText(t.field, t.value, MaxContactTextRunes, false); err != nil {
			return ContactFields{}, err
		}
	}
	if err := validText("notes", out.Notes, MaxContactNotesRunes, true); err != nil {
		return ContactFields{}, err
	}
	if strings.ContainsRune(out.Notes, '\r') {
		return ContactFields{}, fieldError("notes", "contiene un retorno de carro suelto")
	}
	if len(f.Emails) > MaxContactEmails {
		return ContactFields{}, fieldError("emails", "demasiados correos")
	}
	if len(f.Phones) > MaxContactPhones {
		return ContactFields{}, fieldError("phones", "demasiados teléfonos")
	}
	for i, e := range f.Emails {
		field := "emails[" + strconv.Itoa(i) + "]"
		v := strings.TrimSpace(e.Value)
		if hasControl(v, false) || !validEmail(v) {
			return ContactFields{}, fieldError(field+".value", "no es una dirección de correo válida")
		}
		t, err := normalizeType(field+".type", e.Type)
		if err != nil {
			return ContactFields{}, err
		}
		out.Emails = append(out.Emails, TypedValue{Value: v, Type: t})
	}
	for i, p := range f.Phones {
		field := "phones[" + strconv.Itoa(i) + "]"
		v := strings.TrimSpace(p.Value)
		if v == "" {
			return ContactFields{}, fieldError(field+".value", "está vacío")
		}
		if err := validText(field+".value", v, maxPhoneRunes, false); err != nil {
			return ContactFields{}, err
		}
		t, err := normalizeType(field+".type", p.Type)
		if err != nil {
			return ContactFields{}, err
		}
		out.Phones = append(out.Phones, TypedValue{Value: v, Type: t})
	}
	if out.Birthday != "" && birthdayOf(out.Birthday) != out.Birthday {
		return ContactFields{}, fieldError("birthday", "se espera AAAA-MM-DD o --MM-DD")
	}
	if out.Name == "" && out.GivenName == "" && out.FamilyName == "" && out.Organization == "" && len(out.Emails) == 0 && len(out.Phones) == 0 {
		return ContactFields{}, fieldError("name", "el contacto necesita un nombre, una empresa, un correo o un teléfono")
	}
	return out, nil
}

// displayName es el FN que se escribe: el nombre, o lo que mejor lo sustituye.
func (f ContactFields) displayName() string {
	if f.Name != "" {
		return f.Name
	}
	if n := strings.TrimSpace(f.GivenName + " " + f.FamilyName); n != "" {
		return n
	}
	if f.Organization != "" {
		return f.Organization
	}
	if len(f.Emails) > 0 {
		return f.Emails[0].Value
	}
	if len(f.Phones) > 0 {
		return f.Phones[0].Value
	}
	return ""
}

// birthdayOf lleva un BDAY a la forma de la API ("AAAA-MM-DD" o "--MM-DD"); lo que no reconoce (texto
// libre, fechas parciales) da vacio.
func birthdayOf(v string) string {
	v = strings.TrimSpace(v)
	if t, _, ok := strings.Cut(v, "T"); ok {
		v = t
	}
	if rest, ok := strings.CutPrefix(v, "--"); ok {
		rest = strings.ReplaceAll(rest, "-", "")
		if len(rest) != 4 {
			return ""
		}
		if _, err := time.Parse("20060102", "2000"+rest); err != nil {
			return ""
		}
		return "--" + rest[:2] + "-" + rest[2:]
	}
	compact := strings.ReplaceAll(v, "-", "")
	if len(compact) != 8 || (len(v) != 8 && len(v) != 10) {
		return ""
	}
	t, err := time.Parse("20060102", compact)
	if err != nil {
		return ""
	}
	return t.Format("2006-01-02")
}

// formatBirthday escribe el BDAY en la forma de cada version: basica en 4.0 (RFC 6350, 4.3.1) y extendida
// en 3.0 (ISO 8601 como la escriben los clientes de 3.0).
func formatBirthday(v, version string) string {
	if version == vcard4 {
		if rest, ok := strings.CutPrefix(v, "--"); ok {
			return "--" + strings.ReplaceAll(rest, "-", "")
		}
		return strings.ReplaceAll(v, "-", "")
	}
	return v
}

func contactType(l rawLine) string {
	var work, home bool
	for _, t := range l.params["TYPE"] {
		switch strings.ToLower(t) {
		case "cell", "mobile", "iphone":
			return ContactTypeMobile
		case "work":
			work = true
		case "home":
			home = true
		}
	}
	switch {
	case work:
		return ContactTypeWork
	case home:
		return ContactTypeHome
	}
	return ContactTypeOther
}

func textOf(l rawLine) string { return strings.TrimSpace(unescapeText(l.value)) }

func typedOf(l rawLine) TypedValue {
	v := textOf(l)
	if l.name == "TEL" && len(v) > 4 && strings.EqualFold(v[:4], "tel:") {
		v = v[4:]
	}
	return TypedValue{Value: v, Type: contactType(l)}
}

func nameOf(l rawLine) (family, given string) {
	comps := splitEscaped(l.value, ';')
	family = strings.TrimSpace(unescapeText(comps[0]))
	if len(comps) > 1 {
		given = strings.TrimSpace(unescapeText(comps[1]))
	}
	return family, given
}

func orgOf(l rawLine) string {
	return strings.TrimSpace(unescapeText(splitEscaped(l.value, ';')[0]))
}

// cardLines son las propiedades de un vCard ya validado, en orden y sin BEGIN ni END.
func cardLines(raw string) ([]rawLine, string, error) {
	card, err := ParseStoredVCard(raw)
	if err != nil {
		return nil, "", err
	}
	lines := unfold(raw)
	out := make([]rawLine, 0, len(lines))
	for _, text := range lines[1 : len(lines)-1] {
		if l, ok := parseRawLine(text); ok {
			out = append(out, l)
		}
	}
	return out, card.Version, nil
}

// ContactFieldsOf lee un vCard guardado en la forma de la API estructurada.
func ContactFieldsOf(raw string) (ContactFields, error) {
	lines, _, err := cardLines(raw)
	if err != nil {
		return ContactFields{}, err
	}
	var f ContactFields
	seen := map[string]bool{}
	for _, l := range lines {
		first := !seen[l.name]
		seen[l.name] = true
		switch {
		case l.name == "FN" && first:
			f.Name = textOf(l)
		case l.name == "N" && first:
			f.FamilyName, f.GivenName = nameOf(l)
		case l.name == "ORG" && first:
			f.Organization = orgOf(l)
		case l.name == "TITLE" && first:
			f.Title = textOf(l)
		case l.name == "NOTE" && first:
			f.Notes = strings.TrimSpace(unescapeText(l.value))
		case l.name == "BDAY" && first:
			f.Birthday = birthdayOf(l.value)
		case l.name == "EMAIL" && len(f.Emails) < MaxContactEmails:
			if e := typedOf(l); e.Value != "" {
				f.Emails = append(f.Emails, e)
			}
		case l.name == "TEL" && len(f.Phones) < MaxContactPhones:
			if p := typedOf(l); p.Value != "" {
				f.Phones = append(f.Phones, p)
			}
		}
	}
	if f.Name == "" {
		f.Name = strings.TrimSpace(f.GivenName + " " + f.FamilyName)
	}
	return f, nil
}

// typeParam es el parametro TYPE que se escribe para un tipo de la API; other no lleva ninguno.
func typeParam(t, version string) string {
	var v string
	switch t {
	case ContactTypeHome:
		v = "home"
	case ContactTypeWork:
		v = "work"
	case ContactTypeMobile:
		v = "cell"
	default:
		return ""
	}
	if version == vcard3 {
		v = strings.ToUpper(v)
	}
	return ";TYPE=" + v
}

func typedLine(prop string, tv TypedValue, version string) string {
	head := prop
	if prop == "TEL" && version == vcard4 {
		head += ";VALUE=text"
	}
	return head + typeParam(tv.Type, version) + ":" + escapeText(tv.Value)
}

// BuildVCard escribe el vCard de un contacto. Sin existing es una tarjeta 4.0 nueva con ese UID. Con
// existing (un vCard ya guardado) conserva su version, su UID y todas las propiedades que el traductor no
// conoce (PHOTO, X-*, grupos de etiquetas, ...), y de las que conoce reutiliza sin tocar la linea original
// de cada valor que no cambio, con sus parametros. f debe venir normalizado.
func BuildVCard(existing, uid string, f ContactFields, now time.Time) (string, error) {
	version := vcard4
	var lines []rawLine
	if existing != "" {
		var err error
		if lines, version, err = cardLines(existing); err != nil {
			return "", err
		}
	}
	known := map[string][]rawLine{}
	var uidLine *rawLine
	var others []rawLine
	for i, l := range lines {
		switch l.name {
		case "VERSION", "REV":
		case "UID":
			uidLine = &lines[i]
		case "FN", "N", "ORG", "TITLE", "NOTE", "BDAY", "EMAIL", "TEL":
			known[l.name] = append(known[l.name], l)
		default:
			others = append(others, l)
		}
	}

	kept := map[string]bool{}
	dropped := map[string]bool{}
	var w contentWriter
	w.line("BEGIN:VCARD")
	w.line("VERSION:" + version)
	if uidLine != nil {
		w.line(uidLine.text)
	} else {
		w.line("UID:" + escapeText(uid))
	}
	keep := func(ls []rawLine) {
		for _, l := range ls {
			w.line(l.text)
			kept[l.group] = true
		}
	}
	drop := func(ls []rawLine) {
		for _, l := range ls {
			dropped[l.group] = true
		}
	}
	single := func(name, value string, read func(rawLine) string, write func() string) {
		old := known[name]
		if len(old) > 0 && read(old[0]) == value {
			keep(old)
			return
		}
		drop(old)
		if value != "" {
			w.line(write())
		}
	}

	fn := f.displayName()
	single("FN", fn, textOf, func() string { return "FN:" + escapeText(fn) })

	// N cambia solo en apellido y nombre: los otros componentes (segundo nombre, prefijo y sufijo) se conservan.
	// vCard 3.0 exige N aunque vaya vacio.
	func() {
		old := known["N"]
		if len(old) > 0 {
			if family, given := nameOf(old[0]); family == f.FamilyName && given == f.GivenName {
				keep(old)
				return
			}
		}
		drop(old)
		if f.FamilyName == "" && f.GivenName == "" && version != vcard3 {
			return
		}
		comps := []string{"", "", "", "", ""}
		if len(old) > 0 {
			copy(comps, splitEscaped(old[0].value, ';'))
		}
		comps[0], comps[1] = escapeText(f.FamilyName), escapeText(f.GivenName)
		w.line("N:" + strings.Join(comps, ";"))
	}()

	single("ORG", f.Organization, orgOf, func() string { return "ORG:" + escapeText(f.Organization) })
	single("TITLE", f.Title, textOf, func() string { return "TITLE:" + escapeText(f.Title) })
	single("NOTE", f.Notes, func(l rawLine) string { return strings.TrimSpace(unescapeText(l.value)) },
		func() string { return "NOTE:" + escapeText(f.Notes) })
	single("BDAY", f.Birthday, func(l rawLine) string { return birthdayOf(l.value) },
		func() string { return "BDAY:" + formatBirthday(f.Birthday, version) })

	for _, prop := range []struct {
		name   string
		values []TypedValue
	}{{"EMAIL", f.Emails}, {"TEL", f.Phones}} {
		old := known[prop.name]
		used := make([]bool, len(old))
		for _, v := range prop.values {
			reused := false
			for i, l := range old {
				if !used[i] && typedOf(l) == v {
					used[i], reused = true, true
					keep([]rawLine{l})
					break
				}
			}
			if !reused {
				w.line(typedLine(prop.name, v, version))
			}
		}
		for i, l := range old {
			if !used[i] {
				drop([]rawLine{l})
			}
		}
	}

	w.line("REV:" + now.UTC().Format(layoutStamp))
	for _, l := range others {
		// Una etiqueta de grupo (item1.X-ABLabel) solo tiene sentido junto a la propiedad que etiqueta.
		if l.group != "" && dropped[l.group] && !kept[l.group] {
			continue
		}
		w.line(l.text)
	}
	w.line("END:VCARD")
	return w.String(), nil
}
