package domain

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"net/url"
	"path"
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
	// VarImage es la URL de una imagen: solo https y nunca SVG. Gmail pasa cada imagen por su
	// proxy, que no muestra SVG, y una imagen http deja el correo con contenido mixto.
	VarImage = "image"
	// VarList es una lista de objetos de un solo nivel con campos declarados (Fields). Solo se
	// recorre con {{range}}; sus campos son de los tipos simples.
	VarList = "list"
)

func VariableTypes() []string {
	return []string{VarString, VarNumber, VarBoolean, VarURL, VarEmail, VarImage, VarList}
}

// FieldTypes son los tipos que admite un campo de una lista: todos menos la propia lista.
func FieldTypes() []string {
	return []string{VarString, VarNumber, VarBoolean, VarURL, VarEmail, VarImage}
}

// Topes de una lista. MaxListItems es un rechazo, no un recorte: quien envia debe saber que su
// lista no cabe. Lo que se muestra se acota en la plantilla con take.
const (
	MaxListFields = 20
	MaxListItems  = 100
)

// Variable es una variable declarada por la version. Default se guarda tal cual llego en
// JSON para no perder la representacion del numero; se comprueba contra Type al guardar.
// Fields solo existe en una lista.
type Variable struct {
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	Required bool            `json:"required"`
	Default  json.RawMessage `json:"default,omitempty"`
	Fields   []Field         `json:"fields,omitempty"`
}

// Field es un campo de cada elemento de una lista. Un campo opcional ausente vale el cero de
// su tipo, como una variable.
type Field struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

// Field devuelve el campo declarado con ese nombre.
func (v Variable) Field(name string) (Field, bool) {
	for _, f := range v.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
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
			return fmt.Errorf("%w: variables[%d].name %q debe ser un identificador en minúsculas (a-z, 0-9, _) de hasta 64 caracteres", ErrInvalidVariableDeclaration, i, v.Name)
		}
		if IsReserved(v.Name) {
			return fmt.Errorf("%w: %q es una variable reservada y no se declara", ErrInvalidVariableDeclaration, v.Name)
		}
		if _, dup := seen[v.Name]; dup {
			return fmt.Errorf("%w: la variable %q está declarada dos veces", ErrInvalidVariableDeclaration, v.Name)
		}
		seen[v.Name] = struct{}{}
		if !oneOf(v.Type, VariableTypes()) {
			return fmt.Errorf("%w: variables[%d].type %q debe ser uno de: %s", ErrInvalidVariableDeclaration, i, v.Type, strings.Join(VariableTypes(), ", "))
		}
		if err := validateFields(v); err != nil {
			return err
		}
		if len(v.Default) > 0 {
			if v.Type == VarList {
				return fmt.Errorf("%w: la lista %q no admite default", ErrInvalidVariableDeclaration, v.Name)
			}
			if v.Required {
				return fmt.Errorf("%w: la variable %q es requerida y no admite default", ErrInvalidVariableDeclaration, v.Name)
			}
			if _, err := coerceScalar(v.Name, v.Type, v.Default); err != nil {
				return fmt.Errorf("%w: default de %q: %v", ErrInvalidVariableDeclaration, v.Name, err)
			}
		}
	}
	return nil
}

func validateFields(v Variable) error {
	if v.Type != VarList {
		if len(v.Fields) > 0 {
			return fmt.Errorf("%w: solo una lista declara campos (%q es %s)", ErrInvalidVariableDeclaration, v.Name, v.Type)
		}
		return nil
	}
	if len(v.Fields) == 0 || len(v.Fields) > MaxListFields {
		return fmt.Errorf("%w: la lista %q debe declarar entre 1 y %d campos", ErrInvalidVariableDeclaration, v.Name, MaxListFields)
	}
	seen := make(map[string]struct{}, len(v.Fields))
	for j, f := range v.Fields {
		if !variableNameRegex.MatchString(f.Name) {
			return fmt.Errorf("%w: %s.fields[%d].name %q debe ser un identificador en minúsculas (a-z, 0-9, _) de hasta 64 caracteres", ErrInvalidVariableDeclaration, v.Name, j, f.Name)
		}
		if _, dup := seen[f.Name]; dup {
			return fmt.Errorf("%w: el campo %q está declarado dos veces en la lista %q", ErrInvalidVariableDeclaration, f.Name, v.Name)
		}
		seen[f.Name] = struct{}{}
		if !oneOf(f.Type, FieldTypes()) {
			return fmt.Errorf("%w: %s.fields[%d].type %q debe ser uno de: %s", ErrInvalidVariableDeclaration, v.Name, j, f.Type, strings.Join(FieldTypes(), ", "))
		}
	}
	return nil
}

func oneOf(t string, allowed []string) bool {
	for _, a := range allowed {
		if t == a {
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
			val, err := coerce(v, raw)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrInvalidVariables, err)
			}
			out[v.Name] = val
		case len(v.Default) > 0:
			val, err := coerceScalar(v.Name, v.Type, v.Default)
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
	case VarList:
		return []map[string]any{}
	default:
		return ""
	}
}

// jsonNumber es la gramatica de un numero JSON. strconv.ParseFloat admite ademas NaN, Inf,
// hexadecimales y un signo +, que no son importes y que encoding/json no sabe escribir.
var jsonNumber = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][-+]?[0-9]+)?$`)

// coerce interpreta el valor de una variable declarada.
func coerce(v Variable, raw json.RawMessage) (any, error) {
	if v.Type == VarList {
		return coerceList(v, raw)
	}
	return coerceScalar(v.Name, v.Type, raw)
}

// coerceList interpreta una lista: cada elemento es un objeto cuyos campos declarados se
// validan como una variable; los campos no declarados se ignoran, igual que las variables.
func coerceList(v Variable, raw json.RawMessage) ([]map[string]any, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("la variable %q debe ser una lista", v.Name)
	}
	if len(items) > MaxListItems {
		return nil, fmt.Errorf("la lista %q admite hasta %d elementos y trae %d", v.Name, MaxListItems, len(items))
	}
	out := make([]map[string]any, 0, len(items))
	for i, item := range items {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(item, &obj); err != nil || obj == nil {
			return nil, fmt.Errorf("%s[%d] debe ser un objeto", v.Name, i)
		}
		row := make(map[string]any, len(v.Fields))
		for _, f := range v.Fields {
			name := fmt.Sprintf("%s[%d].%s", v.Name, i, f.Name)
			fraw, present := obj[f.Name]
			// Un campo opcional en blanco es un campo ausente: un producto sin imagen suele
			// llegar con "" y no debe tumbar el envio del pedido entero.
			blank := present && !f.Required && strings.TrimSpace(string(fraw)) == `""`
			switch {
			case present && !isJSONNull(fraw) && !blank:
				val, err := coerceScalar(name, f.Type, fraw)
				if err != nil {
					return nil, err
				}
				row[f.Name] = val
			case f.Required:
				return nil, fmt.Errorf("falta el campo requerido %q", name)
			default:
				row[f.Name] = ZeroValue(f.Type)
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// coerce interpreta un valor JSON segun el tipo declarado. Los numeros se conservan como
// json.Number para imprimirlos exactamente como llegaron (sin notacion cientifica ni
// perdida de precision). Se admite el numero o el booleano como cadena porque los
// llamadores que vienen de formularios o de atributos de contacto los traen asi.
func coerceScalar(name, typ string, raw json.RawMessage) (any, error) {
	switch typ {
	case VarString, VarURL, VarEmail, VarImage:
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
				return nil, fmt.Errorf("la variable %q debe ser numérica", name)
			}
			n = json.Number(strings.TrimSpace(s))
		}
		if !jsonNumber.MatchString(n.String()) {
			return nil, fmt.Errorf("la variable %q debe ser numérica", name)
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
	case VarImage:
		u, err := url.Parse(s)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("la variable %q debe ser una URL https de imagen", name)
		}
		if strings.EqualFold(path.Ext(u.Path), ".svg") {
			return fmt.Errorf("la variable %q no puede ser un SVG: Gmail no lo muestra", name)
		}
	case VarURL:
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("la variable %q debe ser una URL absoluta http o https", name)
		}
	case VarEmail:
		addr, err := mail.ParseAddress(s)
		if err != nil || addr.Address != s || !strings.Contains(s, "@") {
			return fmt.Errorf("la variable %q debe ser un correo válido", name)
		}
	}
	return nil
}
