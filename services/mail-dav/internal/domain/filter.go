package domain

import "strings"

// Collation nombra las comparaciones de texto de addressbook-query y calendar-query (RFC 4790).
type Collation string

const (
	CollationUnicodeCasemap Collation = "i;unicode-casemap"
	CollationASCIICasemap   Collation = "i;ascii-casemap"
	CollationOctet          Collation = "i;octet"
)

func (c Collation) Valid() bool {
	return c == CollationUnicodeCasemap || c == CollationASCIICasemap || c == CollationOctet
}

type MatchType string

const (
	MatchContains   MatchType = "contains"
	MatchEquals     MatchType = "equals"
	MatchStartsWith MatchType = "starts-with"
	MatchEndsWith   MatchType = "ends-with"
)

func (m MatchType) Valid() bool {
	return m == MatchContains || m == MatchEquals || m == MatchStartsWith || m == MatchEndsWith
}

type TextMatch struct {
	Text      string
	Collation Collation
	Type      MatchType
	Negate    bool
}

func (m TextMatch) matches(value string) bool {
	text := m.Text
	if m.Collation != CollationOctet {
		value, text = strings.ToLower(value), strings.ToLower(text)
	}
	var ok bool
	switch m.Type {
	case MatchEquals:
		ok = value == text
	case MatchStartsWith:
		ok = strings.HasPrefix(value, text)
	case MatchEndsWith:
		ok = strings.HasSuffix(value, text)
	default:
		ok = strings.Contains(value, text)
	}
	return ok != m.Negate
}

// PropFilter es un prop-filter de addressbook-query o calendar-query: la propiedad debe no existir (IsNotDefined) o
// cumplir sus comparaciones de texto, todas (AllOf) o alguna.
type PropFilter struct {
	Name         string
	AllOf        bool
	IsNotDefined bool
	Matches      []TextMatch
}

func (f PropFilter) matches(props []Property) bool {
	found := false
	for _, p := range props {
		if p.Name != f.Name {
			continue
		}
		found = true
		if f.IsNotDefined {
			return false
		}
		if f.instanceMatches(p.Value) {
			return true
		}
	}
	return f.IsNotDefined && !found
}

// instanceMatches evalua las comparaciones sobre el valor de UNA aparicion de la propiedad: la
// propiedad cumple el filtro si alguna de sus apariciones lo cumple.
func (f PropFilter) instanceMatches(value string) bool {
	if len(f.Matches) == 0 {
		return true
	}
	for _, m := range f.Matches {
		ok := m.matches(value)
		if f.AllOf && !ok {
			return false
		}
		if !f.AllOf && ok {
			return true
		}
	}
	return f.AllOf
}

// Filter es el filtro de un addressbook-query. Sin prop-filters admite todo.
type Filter struct {
	AllOf bool
	Props []PropFilter
}

func (f Filter) Matches(c Card) bool {
	if len(f.Props) == 0 {
		return true
	}
	for _, pf := range f.Props {
		ok := pf.matches(c.Props)
		if f.AllOf && !ok {
			return false
		}
		if !f.AllOf && ok {
			return true
		}
	}
	return f.AllOf
}
