package domain

import (
	"bytes"
	"encoding/json"
	"regexp"

	"github.com/google/uuid"
)

// ConditionKind es la lista blanca de condiciones de una rama.
type ConditionKind string

const (
	// ConditionEmailOpened y ConditionEmailClicked miran el correo que envio un paso
	// send_email anterior del mismo recorrido (Step). Un clic cuenta como apertura. La
	// apertura la registra el pixel de seguimiento: Apple Mail (Mail Privacy Protection) y
	// algunos antivirus lo descargan sin que la persona abra, asi que "abrio" puede ser
	// cierto sin serlo; el clic es la senal fiable.
	ConditionEmailOpened  ConditionKind = "email_opened"
	ConditionEmailClicked ConditionKind = "email_clicked"
	// ConditionSegment: el contacto cumple hoy un segmento guardado de contacts.
	ConditionSegment ConditionKind = "segment"
	// ConditionAttribute: un atributo declarado del contacto cumple Op y Value con las
	// reglas del DSL de segmentos de contacts, que es quien lo valida y lo evalua.
	ConditionAttribute ConditionKind = "attribute"
)

func ConditionKinds() []ConditionKind {
	return []ConditionKind{ConditionEmailOpened, ConditionEmailClicked, ConditionSegment, ConditionAttribute}
}

// ReferencesStep dice si la condicion mira el correo de otro paso.
func (k ConditionKind) ReferencesStep() bool {
	return k == ConditionEmailOpened || k == ConditionEmailClicked
}

// MaxConditionValueBytes acota el valor de una condicion de atributo.
const MaxConditionValueBytes = 8 << 10

var (
	attributeKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	conditionOpPattern  = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)
)

// Condition es la condicion de un paso branch. Cada tipo usa solo sus campos.
type Condition struct {
	Kind      ConditionKind   `json:"kind"`
	Step      string          `json:"step,omitempty"`
	SegmentID *uuid.UUID      `json:"segment_id,omitempty"`
	Attribute string          `json:"attribute,omitempty"`
	Op        string          `json:"op,omitempty"`
	Value     json.RawMessage `json:"value,omitempty"`
}

func (c *Condition) normalize(field string) error {
	hasAttr := c.Attribute != "" || c.Op != "" || len(c.Value) > 0
	switch c.Kind {
	case ConditionEmailOpened, ConditionEmailClicked:
		if c.SegmentID != nil || hasAttr {
			return NewValidationError("%skind %s solo admite step", field, c.Kind)
		}
		if c.Step == "" {
			return NewValidationError("%sstep es obligatorio: el paso de envio cuyo correo se mira", field)
		}
	case ConditionSegment:
		if c.Step != "" || hasAttr {
			return NewValidationError("%skind segment solo admite segment_id", field)
		}
		if c.SegmentID == nil || *c.SegmentID == uuid.Nil {
			return NewValidationError("%ssegment_id es obligatorio", field)
		}
	case ConditionAttribute:
		if c.Step != "" || c.SegmentID != nil {
			return NewValidationError("%skind attribute solo admite attribute, op y value", field)
		}
		if !attributeKeyPattern.MatchString(c.Attribute) {
			return NewValidationError("%sattribute debe ser la clave de un atributo declarado", field)
		}
		if !conditionOpPattern.MatchString(c.Op) {
			return NewValidationError("%sop no es valido", field)
		}
		if len(c.Value) > MaxConditionValueBytes {
			return NewValidationError("%svalue supera %d KB", field, MaxConditionValueBytes>>10)
		}
		if len(c.Value) > 0 {
			var buf bytes.Buffer
			if err := json.Compact(&buf, c.Value); err != nil {
				return NewValidationError("%svalue no es JSON valido", field)
			}
			c.Value = buf.Bytes()
			if bytes.Equal(c.Value, []byte("null")) {
				c.Value = nil
			}
		}
	default:
		return NewValidationError("%skind debe ser email_opened, email_clicked, segment o attribute", field)
	}
	return nil
}

// AttributeDefinition es la condicion de atributo como definicion del DSL de segmentos de
// contacts: un grupo con una sola regla sobre attributes.<clave>.
func (c Condition) AttributeDefinition() json.RawMessage {
	rule := map[string]any{"field": "attributes." + c.Attribute, "op": c.Op}
	if len(c.Value) > 0 {
		rule["value"] = c.Value
	}
	b, _ := json.Marshal(map[string]any{"match": "all", "rules": []any{rule}})
	return b
}

func (c Condition) clone() Condition {
	out := c
	if c.SegmentID != nil {
		id := *c.SegmentID
		out.SegmentID = &id
	}
	if c.Value != nil {
		out.Value = append(json.RawMessage(nil), c.Value...)
	}
	return out
}
