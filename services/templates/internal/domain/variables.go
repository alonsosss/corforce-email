package domain

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
)

// Tipos de variable admitidos. Los valores se validan contra ellos al renderizar.
const (
	VarString  = "string"
	VarNumber  = "number"
	VarBoolean = "boolean"
	VarURL     = "url"
	VarEmail   = "email"
)

func VariableTypes() []string { return []string{VarString, VarNumber, VarBoolean, VarURL, VarEmail} }

// Variable es una variable declarada por la version. Default se guarda tal cual llego en
// JSON para no perder la representacion del numero; se comprueba contra Type al guardar.
type Variable struct {
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	Required bool            `json:"required"`
	Default  json.RawMessage `json:"default,omitempty"`
}

// Variables reservadas: existen siempre al renderizar y las inyecta quien llama
// (transactional, campaigns). No se declaran en la version y, si el llamador no las
// provee, valen cadena vacia para que una previsualizacion no falle por su ausencia.
const (
	ReservedUnsubscribeURL   = "unsubscribe_url"
	ReservedViewInBrowserURL = "view_in_browser_url"
	ReservedRecipientEmail   = "recipient_email"
	ReservedTenantName       = "tenant_name"
)

var reservedVariables = []Variable{
	{Name: ReservedUnsubscribeURL, Type: VarURL},
	{Name: ReservedViewInBrowserURL, Type: VarURL},
	{Name: ReservedRecipientEmail, Type: VarEmail},
	{Name: ReservedTenantName, Type: VarString},
}

// ReservedVariables devuelve la lista de variables reservadas, en orden estable.
func ReservedVariables() []Variable { return append([]Variable(nil), reservedVariables...) }

// IsReserved indica si el nombre pertenece a una variable reservada.
func IsReserved(name string) bool {
	for _, r := range reservedVariables {
		if r.Name == name {
			return true
		}
	}
	return false
}

// variableNameRegex: identificador valido para {{.nombre}} en text/template y sin
// mayusculas, para que el nombre en el JSON del llamador y en la plantilla coincidan.
var variableNameRegex = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ValidateDeclarations comprueba la lista de variables declaradas de una version.
func ValidateDeclarations(vars []Variable) error {
	seen := make(map[string]struct{}, len(vars))
	for i, v := range vars {
		if !variableNameRegex.MatchString(v.Name) {
			return fmt.Errorf("%w: variables[%d].name %q debe ser un identificador en minusculas (a-z, 0-9, _) de hasta 64 caracteres", ErrInvalidVariableDeclaration, i, v.Name)
		}
		if IsReserved(v.Name) {
			return fmt.Errorf("%w: %q es una variable reservada y no se declara", ErrInvalidVariableDeclaration, v.Name)
		}
		if _, dup := seen[v.Name]; dup {
			return fmt.Errorf("%w: la variable %q esta declarada dos veces", ErrInvalidVariableDeclaration, v.Name)
		}
		seen[v.Name] = struct{}{}
		if !validType(v.Type) {
			return fmt.Errorf("%w: variables[%d].type %q debe ser uno de: %s", ErrInvalidVariableDeclaration, i, v.Type, strings.Join(VariableTypes(), ", "))
		}
		if len(v.Default) > 0 {
			if v.Required {
				return fmt.Errorf("%w: la variable %q es requerida y no admite default", ErrInvalidVariableDeclaration, v.Name)
			}
			if _, err := coerce(v.Name, v.Type, v.Default); err != nil {
				return fmt.Errorf("%w: default de %q: %v", ErrInvalidVariableDeclaration, v.Name, err)
			}
		}
	}
	return nil
}

func validType(t string) bool {
	for _, allowed := range VariableTypes() {
		if t == allowed {
			return true
		}
	}
	return false
}

// ResolveValues construye el mapa que recibe la plantilla: valida cada valor recibido
// contra su tipo, aplica defaults y exige las requeridas. Los valores no declarados se
// ignoran para que un llamador con un contexto mas amplio no rompa al cambiar la
// plantilla. Las reservadas ausentes valen cadena vacia.
func ResolveValues(declared []Variable, values map[string]json.RawMessage, reserved map[string]string) (map[string]any, error) {
	out := make(map[string]any, len(declared)+len(reservedVariables))
	for _, v := range declared {
		raw, present := values[v.Name]
		switch {
		case present && !isJSONNull(raw):
			val, err := coerce(v.Name, v.Type, raw)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrInvalidVariables, err)
			}
			out[v.Name] = val
		case len(v.Default) > 0:
			val, err := coerce(v.Name, v.Type, v.Default)
			if err != nil {
				return nil, fmt.Errorf("%w: default de %q: %v", ErrInvalidVariables, v.Name, err)
			}
			out[v.Name] = val
		case v.Required:
			return nil, fmt.Errorf("%w: falta la variable requerida %q", ErrInvalidVariables, v.Name)
		default:
			out[v.Name] = ZeroValue(v.Type)
		}
	}
	for _, r := range reservedVariables {
		val := reserved[r.Name]
		if val != "" {
			if err := checkString(r.Name, r.Type, val); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrInvalidVariables, err)
			}
		}
		out[r.Name] = val
	}
	return out, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

// ZeroValue es lo que ve la plantilla cuando una variable opcional sin default no llega:
// un valor del tipo declarado, nunca nil, para que {{if .x}} y {{.x}} sean predecibles.
func ZeroValue(typ string) any {
	switch typ {
	case VarNumber:
		return json.Number("0")
	case VarBoolean:
		return false
	default:
		return ""
	}
}

// coerce interpreta un valor JSON segun el tipo declarado. Los numeros se conservan como
// json.Number para imprimirlos exactamente como llegaron (sin notacion cientifica ni
// perdida de precision). Se admite el numero o el booleano como cadena porque los
// llamadores que vienen de formularios o de atributos de contacto los traen asi.
func coerce(name, typ string, raw json.RawMessage) (any, error) {
	switch typ {
	case VarString, VarURL, VarEmail:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("la variable %q debe ser una cadena", name)
		}
		if err := checkString(name, typ, s); err != nil {
			return nil, err
		}
		return s, nil
	case VarNumber:
		var n json.Number
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		if err := dec.Decode(&n); err != nil {
			var s string
			if json.Unmarshal(raw, &s) != nil {
				return nil, fmt.Errorf("la variable %q debe ser numerica", name)
			}
			n = json.Number(strings.TrimSpace(s))
		}
		if _, err := n.Float64(); err != nil {
			return nil, fmt.Errorf("la variable %q debe ser numerica", name)
		}
		return n, nil
	case VarBoolean:
		var b bool
		if err := json.Unmarshal(raw, &b); err == nil {
			return b, nil
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "true":
				return true, nil
			case "false":
				return false, nil
			}
		}
		return nil, fmt.Errorf("la variable %q debe ser booleana", name)
	}
	return nil, fmt.Errorf("la variable %q tiene un tipo desconocido %q", name, typ)
}

// checkString valida una cadena contra los tipos que imponen formato. Una URL debe ser
// absoluta y http o https: es lo que impide que un valor acabe como javascript: en un href.
func checkString(name, typ, s string) error {
	switch typ {
	case VarURL:
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("la variable %q debe ser una URL absoluta http o https", name)
		}
	case VarEmail:
		addr, err := mail.ParseAddress(s)
		if err != nil || addr.Address != s || !strings.Contains(s, "@") {
			return fmt.Errorf("la variable %q debe ser un correo valido", name)
		}
	}
	return nil
}
