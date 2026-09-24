package domain

import (
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// StepType es la lista blanca de pasos de un flujo. Un flujo es un grafo acotado sin
// ciclos (graph.go): cada paso sigue en next y una rama (branch) en then o en else.
type StepType string

const (
	StepWait           StepType = "wait"
	StepSendEmail      StepType = "send_email"
	StepAddToList      StepType = "add_to_list"
	StepRemoveFromList StepType = "remove_from_list"
	StepBranch         StepType = "branch"
)

func StepTypes() []StepType {
	return []StepType{StepWait, StepSendEmail, StepAddToList, StepRemoveFromList, StepBranch}
}

const (
	MinSteps = 1
	MaxSteps = 40
	// MaxDepth es el maximo de pasos de cualquier recorrido de principio a fin. Un flujo
	// lineal anterior a las ramas (hasta 20 pasos) sigue cabiendo.
	MaxDepth = 20
	MinWait  = time.Minute
	MaxWait  = 90 * 24 * time.Hour
)

// Step es un paso. Cada tipo usa solo sus campos; el resto debe llegar vacio. ID es su
// nombre en el grafo; Next, el paso que sigue ("" = fin). Una rama no usa Next: sigue en
// Then si la condicion se cumple y en Else si no ("" = fin).
type Step struct {
	ID              string     `json:"id,omitempty"`
	Type            StepType   `json:"type"`
	Duration        string     `json:"duration,omitempty"`
	TemplateID      *uuid.UUID `json:"template_id,omitempty"`
	TemplateVersion *int       `json:"template_version,omitempty"`
	FromEmail       string     `json:"from_email,omitempty"`
	FromName        string     `json:"from_name,omitempty"`
	ReplyTo         string     `json:"reply_to,omitempty"`
	ListID          *uuid.UUID `json:"list_id,omitempty"`
	Condition       *Condition `json:"condition,omitempty"`
	Next            string     `json:"next,omitempty"`
	Then            string     `json:"then,omitempty"`
	Else            string     `json:"else,omitempty"`
}

// WaitUnit es una unidad admitida en la duracion de una espera.
type WaitUnit struct {
	Code     string
	Duration time.Duration
}

// WaitUnits son las unidades de una espera, de menor a mayor. Se admite d porque las
// esperas de marketing se piensan en dias y time.ParseDuration no lo entiende.
func WaitUnits() []WaitUnit {
	return []WaitUnit{{Code: "m", Duration: time.Minute}, {Code: "h", Duration: time.Hour}, {Code: "d", Duration: 24 * time.Hour}}
}

// durationPattern: un entero y una unidad de WaitUnits.
var durationPattern = regexp.MustCompile(`^([1-9][0-9]{0,6})([a-z])$`)

func waitUnit(code string) (time.Duration, bool) {
	for _, u := range WaitUnits() {
		if u.Code == code {
			return u.Duration, true
		}
	}
	return 0, false
}

// ParseWait interpreta la duracion de una espera y exige que quede entre MinWait y MaxWait.
func ParseWait(s string) (time.Duration, error) {
	m := durationPattern.FindStringSubmatch(s)
	var unit time.Duration
	known := false
	if m != nil {
		unit, known = waitUnit(m[2])
	}
	if !known {
		return 0, NewValidationError("duration debe ser un entero con unidad m, h o d (por ejemplo 30m, 12h, 3d)")
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, NewValidationError("duration no valida")
	}
	if int64(n) > int64(MaxWait/unit) {
		return 0, NewValidationError("duration no puede superar 90d")
	}
	d := time.Duration(n) * unit
	if d < MinWait {
		return 0, NewValidationError("duration debe ser de al menos 1m")
	}
	return d, nil
}

// WaitDuration es la espera de un paso wait ya validado.
func (s Step) WaitDuration() time.Duration {
	d, _ := ParseWait(s.Duration)
	return d
}

func (s *Step) normalize(i int) error {
	field := "steps[" + strconv.Itoa(i) + "]."
	hasSend := s.TemplateID != nil || s.TemplateVersion != nil || s.FromEmail != "" || s.FromName != "" || s.ReplyTo != ""
	if s.Type != StepBranch && (s.Condition != nil || s.Then != "" || s.Else != "") {
		return NewValidationError("%s: condition, then y else solo aplican a un paso branch", field+"type")
	}
	switch s.Type {
	case StepBranch:
		if hasSend || s.ListID != nil || s.Duration != "" {
			return NewValidationError("%s: un paso branch solo admite condition, then y else", field+"type")
		}
		if s.Next != "" {
			return NewValidationError("%snext no aplica a un paso branch: sigue en then o en else", field)
		}
		if s.Condition == nil {
			return NewValidationError("%scondition es obligatoria", field)
		}
		if err := s.Condition.normalize(field + "condition."); err != nil {
			return err
		}
	case StepWait:
		if hasSend || s.ListID != nil {
			return NewValidationError("%s: un paso wait solo admite duration", field+"type")
		}
		if _, err := ParseWait(s.Duration); err != nil {
			return NewValidationError("%s%s", field, err.Error())
		}
	case StepSendEmail:
		if s.Duration != "" || s.ListID != nil {
			return NewValidationError("%s: un paso send_email no admite duration ni list_id", field+"type")
		}
		if s.TemplateID == nil || *s.TemplateID == uuid.Nil {
			return NewValidationError("%stemplate_id es obligatorio", field)
		}
		if s.TemplateVersion != nil && *s.TemplateVersion < 1 {
			return NewValidationError("%stemplate_version debe ser mayor que cero", field)
		}
		if err := normalizeSender(field, &s.FromEmail, &s.FromName, &s.ReplyTo); err != nil {
			return err
		}
	case StepAddToList, StepRemoveFromList:
		if hasSend || s.Duration != "" {
			return NewValidationError("%s: un paso de lista solo admite list_id", field+"type")
		}
		if s.ListID == nil || *s.ListID == uuid.Nil {
			return NewValidationError("%slist_id es obligatorio", field)
		}
	default:
		return NewValidationError("%stype debe ser wait, send_email, add_to_list, remove_from_list o branch", field)
	}
	return nil
}

// cloneSteps copia la lista con sus punteros, para que editar una copia no altere otra.
func cloneSteps(in []Step) []Step {
	out := make([]Step, len(in))
	for i, s := range in {
		out[i] = s
		if s.TemplateID != nil {
			id := *s.TemplateID
			out[i].TemplateID = &id
		}
		if s.TemplateVersion != nil {
			v := *s.TemplateVersion
			out[i].TemplateVersion = &v
		}
		if s.ListID != nil {
			id := *s.ListID
			out[i].ListID = &id
		}
		if s.Condition != nil {
			c := s.Condition.clone()
			out[i].Condition = &c
		}
	}
	return out
}
