package domain

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	MaxVacationSubjectRunes = 200
	MaxVacationMessageRunes = 8192
	MinVacationIntervalDays = 1
	MaxVacationIntervalDays = 30
	// DefaultVacationIntervalDays: una respuesta por remitente y dia, lo que evita que dos buzones
	// con respuesta automatica se contesten en bucle y que un remitente reciba el mismo aviso
	// veinte veces.
	DefaultVacationIntervalDays = 1

	vacationDateLayout = "2006-01-02"
)

// VacationReply es la respuesta automatica de un buzon. Las fechas son de calendario, sin hora:
// Dovecot las compara con la fecha del servidor (UTC).
type VacationReply struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	Username     string
	Enabled      bool
	Subject      string
	Message      string
	IntervalDays int
	StartsOn     *time.Time
	EndsOn       *time.Time
	// ScriptData es el script Sieve generado por Normalize; la vista v_sieve_vacation lo sirve a
	// Dovecot. Vacio mientras la respuesta esta desactivada.
	ScriptData string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// NewVacationReply es lo que ve un buzon que nunca configuro nada: desactivada, sin texto.
func NewVacationReply(tenantID uuid.UUID, username string) *VacationReply {
	return &VacationReply{TenantID: tenantID, Username: username, IntervalDays: DefaultVacationIntervalDays}
}

// ParseVacationDate lee una fecha AAAA-MM-DD. Una cadena vacia es "sin fecha".
func ParseVacationDate(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(vacationDateLayout, raw)
	if err != nil {
		return nil, ErrVacationDate
	}
	return &t, nil
}

// FormatVacationDate es la inversa de ParseVacationDate.
func FormatVacationDate(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(vacationDateLayout)
	return &s
}

// Normalize limpia y valida los campos y genera el script. Los errores son de entrada (422).
func (v *VacationReply) Normalize() error {
	v.Subject = strings.TrimSpace(v.Subject)
	if !validVacationText(v.Subject, MaxVacationSubjectRunes, false) {
		return ErrVacationSubjectInvalid
	}
	v.Message = strings.TrimSpace(strings.ReplaceAll(v.Message, "\r\n", "\n"))
	if !validVacationText(v.Message, MaxVacationMessageRunes, true) {
		return ErrVacationMessageInvalid
	}
	if v.IntervalDays == 0 {
		v.IntervalDays = DefaultVacationIntervalDays
	}
	if v.IntervalDays < MinVacationIntervalDays || v.IntervalDays > MaxVacationIntervalDays {
		return ErrVacationInterval
	}
	if v.StartsOn != nil && v.EndsOn != nil && v.EndsOn.Before(*v.StartsOn) {
		return ErrVacationWindow
	}
	if !v.Enabled {
		v.ScriptData = ""
		return nil
	}
	if v.Message == "" {
		return ErrVacationMessageRequired
	}
	v.ScriptData = buildVacationScript(v)
	return nil
}

// validVacationText: UTF-8 valido, dentro del tope y sin caracteres de control. Un mensaje admite
// ademas el salto de linea y el tabulador; el asunto va en una cabecera y no admite ninguno.
func validVacationText(s string, maxRunes int, multiline bool) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxRunes {
		return false
	}
	for _, r := range s {
		if r == '\n' || r == '\t' {
			if !multiline {
				return false
			}
			continue
		}
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return false
		}
	}
	return true
}

// sieveQuote escribe s como cadena entre comillas de Sieve (RFC 5228, 2.4.2): la barra y la comilla
// se escapan y el salto de linea va como CRLF, que es lo que admite la gramatica.
func sieveQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", "\r\n")
	return `"` + s + `"`
}

// buildVacationScript genera el script de una respuesta activa. Con ventana de fechas lo envuelve en
// una prueba de currentdate (extensiones date y relational); sin ella, es solo la accion vacation.
// El texto del usuario solo entra como cadena entre comillas, nunca como codigo Sieve.
func buildVacationScript(v *VacationReply) string {
	var tests []string
	if v.StartsOn != nil {
		tests = append(tests, `currentdate :value "ge" "date" `+sieveQuote(v.StartsOn.Format(vacationDateLayout)))
	}
	if v.EndsOn != nil {
		tests = append(tests, `currentdate :value "le" "date" `+sieveQuote(v.EndsOn.Format(vacationDateLayout)))
	}

	action := "vacation :days " + strconv.Itoa(v.IntervalDays)
	if v.Subject != "" {
		action += " :subject " + sieveQuote(v.Subject)
	}
	action += " " + sieveQuote(v.Message) + ";"

	var b strings.Builder
	b.WriteString("# Respuesta automatica generada por la plataforma; se edita desde el buzon, no a mano.\n")
	switch len(tests) {
	case 0:
		b.WriteString("require [\"vacation\"];\n")
		b.WriteString(action + "\n")
	case 1:
		b.WriteString("require [\"vacation\", \"date\", \"relational\"];\n")
		b.WriteString("if " + tests[0] + " {\n  " + action + "\n}\n")
	default:
		b.WriteString("require [\"vacation\", \"date\", \"relational\"];\n")
		b.WriteString("if allof (\n  " + strings.Join(tests, ",\n  ") + "\n) {\n  " + action + "\n}\n")
	}
	return b.String()
}
