package domain

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Topes de las reglas de un buzon. La interfaz los lee de la respuesta (limits) y no los copia.
const (
	MaxFilterRules       = 50
	MaxFilterConditions  = 10
	MaxFilterActions     = 5
	MaxForwardAddresses  = 5
	MaxFilterValueRunes  = 256
	MaxFilterNameRunes   = 100
	MaxFilterFolderBytes = 512
	// MaxFilterScriptBytes es sieve_max_script_size de deploy/mail/dovecot/conf/dovecot.conf: un script
	// mas grande Dovecot no lo compila y el buzon se quedaria sin filtrar.
	MaxFilterScriptBytes = 1 << 20
)

const (
	FilterMatchAll = "all"
	FilterMatchAny = "any"

	FilterFieldFrom      = "from"
	FilterFieldTo        = "to"
	FilterFieldCc        = "cc"
	FilterFieldRecipient = "recipient"
	FilterFieldSubject   = "subject"

	FilterOpContains    = "contains"
	FilterOpNotContains = "not_contains"
	FilterOpIs          = "is"

	FilterActionMove     = "move"
	FilterActionMarkRead = "mark_read"
	FilterActionFlag     = "flag"
	FilterActionForward  = "forward"
	FilterActionDiscard  = "discard"
)

// filterHeaders es la lista de cabeceras Sieve que mira cada campo; recipient es cualquiera de los
// destinatarios visibles.
var filterHeaders = map[string]string{
	FilterFieldFrom:      `"from"`,
	FilterFieldTo:        `"to"`,
	FilterFieldCc:        `"cc"`,
	FilterFieldRecipient: `["to", "cc"]`,
	FilterFieldSubject:   `"subject"`,
}

type FilterCondition struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// FilterAction: Folder solo con move; Address y KeepCopy solo con forward.
type FilterAction struct {
	Type     string `json:"type"`
	Folder   string `json:"folder,omitempty"`
	Address  string `json:"address,omitempty"`
	KeepCopy bool   `json:"keep_copy,omitempty"`
}

type FilterRule struct {
	ID         uuid.UUID         `json:"id"`
	Name       string            `json:"name"`
	Enabled    bool              `json:"enabled"`
	Match      string            `json:"match"`
	Conditions []FilterCondition `json:"conditions"`
	Actions    []FilterAction    `json:"actions"`
	Stop       bool              `json:"stop"`
}

// Forwarding es el reenvio de todo el correo entrante (salvo spam) del buzon.
type Forwarding struct {
	Enabled   bool     `json:"enabled"`
	Addresses []string `json:"addresses"`
	KeepCopy  bool     `json:"keep_copy"`
}

// MailboxFilters son las reglas y el reenvio de un buzon. Se guardan estructurados y el directorio
// genera el script Sieve que Dovecot lee por mail.v_sieve_user.
type MailboxFilters struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	Username   string
	Rules      []FilterRule
	Forwarding Forwarding
	// ScriptData es el script generado por Normalize; vacio si no hay nada activo.
	ScriptData string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// NewMailboxFilters es lo que ve un buzon que nunca guardo reglas.
func NewMailboxFilters(tenantID uuid.UUID, username string) *MailboxFilters {
	return &MailboxFilters{
		TenantID: tenantID, Username: username, Rules: []FilterRule{}, Forwarding: Forwarding{Addresses: []string{}},
	}
}

// Normalize valida y limpia las reglas y el reenvio y genera el script. Username tiene que venir ya
// resuelto: el reenvio nunca apunta al propio buzon. Los errores son *FieldError.
func (f *MailboxFilters) Normalize() error {
	if f.Rules == nil {
		f.Rules = []FilterRule{}
	}
	if len(f.Rules) > MaxFilterRules {
		return fieldErr("rules", "supera el maximo de "+strconv.Itoa(MaxFilterRules)+" reglas")
	}
	seen := make(map[uuid.UUID]struct{}, len(f.Rules))
	for i := range f.Rules {
		prefix := "rules[" + strconv.Itoa(i) + "]"
		r := &f.Rules[i]
		if r.ID == uuid.Nil {
			r.ID = uuid.New()
		}
		if _, dup := seen[r.ID]; dup {
			return fieldErr(prefix+".id", "esta repetido")
		}
		seen[r.ID] = struct{}{}
		if err := r.normalize(prefix, f.Username); err != nil {
			return err
		}
	}
	if err := f.Forwarding.normalize(f.Username); err != nil {
		return err
	}
	f.ScriptData = buildFilterScript(f)
	if len(f.ScriptData) > MaxFilterScriptBytes {
		return fieldErr("rules", "el conjunto de reglas es demasiado grande")
	}
	return nil
}

func (r *FilterRule) normalize(prefix, username string) error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return fieldErr(prefix+".name", "es obligatorio")
	}
	if !validFilterLine(r.Name, MaxFilterNameRunes) {
		return fieldErr(prefix+".name", "tiene caracteres no validos o supera los "+strconv.Itoa(MaxFilterNameRunes))
	}
	switch r.Match {
	case "":
		r.Match = FilterMatchAll
	case FilterMatchAll, FilterMatchAny:
	default:
		return fieldErr(prefix+".match", "debe ser all o any")
	}
	if len(r.Conditions) == 0 {
		return fieldErr(prefix+".conditions", "hace falta al menos una condicion")
	}
	if len(r.Conditions) > MaxFilterConditions {
		return fieldErr(prefix+".conditions", "supera el maximo de "+strconv.Itoa(MaxFilterConditions)+" condiciones")
	}
	for j := range r.Conditions {
		if err := r.Conditions[j].normalize(prefix + ".conditions[" + strconv.Itoa(j) + "]"); err != nil {
			return err
		}
	}
	if len(r.Actions) == 0 {
		return fieldErr(prefix+".actions", "hace falta al menos una accion")
	}
	if len(r.Actions) > MaxFilterActions {
		return fieldErr(prefix+".actions", "supera el maximo de "+strconv.Itoa(MaxFilterActions)+" acciones")
	}
	count := map[string]int{}
	forwards := map[string]struct{}{}
	for j := range r.Actions {
		field := prefix + ".actions[" + strconv.Itoa(j) + "]"
		a := &r.Actions[j]
		if err := a.normalize(field, username); err != nil {
			return err
		}
		count[a.Type]++
		if a.Type == FilterActionForward {
			if _, dup := forwards[a.Address]; dup {
				return fieldErr(field+".address", "esta repetida en la regla")
			}
			forwards[a.Address] = struct{}{}
		} else if count[a.Type] > 1 {
			return fieldErr(field+".type", "esta repetida en la regla")
		}
		// Descartar y a la vez archivar o marcar no tiene sentido: el mensaje no se guarda en ningun sitio.
		if count[FilterActionDiscard] > 0 && count[FilterActionMove]+count[FilterActionMarkRead]+count[FilterActionFlag] > 0 {
			return fieldErr(field+".type", "discard no se combina con move, mark_read ni flag")
		}
	}
	return nil
}

func (c *FilterCondition) normalize(prefix string) error {
	if _, ok := filterHeaders[c.Field]; !ok {
		return fieldErr(prefix+".field", "debe ser from, to, cc, recipient o subject")
	}
	switch c.Op {
	case FilterOpContains, FilterOpNotContains, FilterOpIs:
	default:
		return fieldErr(prefix+".op", "debe ser contains, not_contains o is")
	}
	c.Value = strings.TrimSpace(c.Value)
	if c.Value == "" {
		return fieldErr(prefix+".value", "es obligatorio")
	}
	if !validFilterLine(c.Value, MaxFilterValueRunes) {
		return fieldErr(prefix+".value", "tiene caracteres no validos o supera los "+strconv.Itoa(MaxFilterValueRunes))
	}
	return nil
}

func (a *FilterAction) normalize(prefix, username string) error {
	a.Folder = strings.TrimSpace(a.Folder)
	a.Address = strings.TrimSpace(a.Address)
	switch a.Type {
	case FilterActionMove:
		if a.Address != "" || a.KeepCopy {
			return fieldErr(prefix+".type", "move solo lleva folder")
		}
		folder, err := normalizeFilterFolder(a.Folder)
		if err != nil {
			return fieldErr(prefix+".folder", err.Error())
		}
		a.Folder = folder
	case FilterActionForward:
		if a.Folder != "" {
			return fieldErr(prefix+".type", "forward solo lleva address y keep_copy")
		}
		addr, err := normalizeForwardAddress(a.Address, username)
		if err != nil {
			return fieldErr(prefix+".address", err.Error())
		}
		a.Address = addr
	case FilterActionMarkRead, FilterActionFlag, FilterActionDiscard:
		if a.Folder != "" || a.Address != "" || a.KeepCopy {
			return fieldErr(prefix+".type", a.Type+" no lleva parametros")
		}
	default:
		return fieldErr(prefix+".type", "debe ser move, mark_read, flag, forward o discard")
	}
	return nil
}

// normalize valida el reenvio. Las direcciones de un reenvio apagado tambien se validan: se guardan
// para que el usuario lo vuelva a encender sin escribirlas de nuevo.
func (fw *Forwarding) normalize(username string) error {
	if fw.Addresses == nil {
		fw.Addresses = []string{}
	}
	if len(fw.Addresses) > MaxForwardAddresses {
		return fieldErr("forwarding.addresses", "supera el maximo de "+strconv.Itoa(MaxForwardAddresses)+" direcciones")
	}
	seen := make(map[string]struct{}, len(fw.Addresses))
	for i, raw := range fw.Addresses {
		field := "forwarding.addresses[" + strconv.Itoa(i) + "]"
		addr, err := normalizeForwardAddress(raw, username)
		if err != nil {
			return fieldErr(field, err.Error())
		}
		if _, dup := seen[addr]; dup {
			return fieldErr(field, "esta repetida")
		}
		seen[addr] = struct{}{}
		fw.Addresses[i] = addr
	}
	if fw.Enabled && len(fw.Addresses) == 0 {
		return fieldErr("forwarding.addresses", "hace falta al menos una direccion")
	}
	return nil
}

type filterReason string

func (r filterReason) Error() string { return string(r) }

func normalizeForwardAddress(raw, username string) (string, error) {
	addr, _, err := NormalizeEmail(raw)
	if err != nil {
		return "", filterReason("no es una direccion de correo valida")
	}
	if addr == strings.ToLower(username) {
		return "", filterReason("no puede ser el propio buzon")
	}
	return addr, nil
}

// normalizeFilterFolder aplica a la carpeta de destino las reglas de nombre de Dovecot (separador /):
// sin niveles vacios, sin comodines IMAP y sin caracteres de control.
func normalizeFilterFolder(folder string) (string, error) {
	if folder == "" {
		return "", filterReason("es obligatoria")
	}
	if len(folder) > MaxFilterFolderBytes || !utf8.ValidString(folder) {
		return "", filterReason("es demasiado larga o no es UTF-8 valido")
	}
	for _, r := range folder {
		if unicode.IsControl(r) || r == '*' || r == '%' {
			return "", filterReason("contiene caracteres no validos")
		}
	}
	for _, level := range strings.Split(folder, "/") {
		if strings.TrimSpace(level) == "" {
			return "", filterReason("tiene un nivel vacio")
		}
	}
	return folder, nil
}

// validFilterLine: UTF-8 valido, una sola linea, sin caracteres de control ni separadores de linea
// Unicode, dentro del tope.
func validFilterLine(s string, maxRunes int) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxRunes {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return false
		}
	}
	return true
}

// Orden fijo de las extensiones en el require: el mismo conjunto de reglas da siempre el mismo
// script (y el mismo id md5 en la vista).
var filterExtensions = []string{"fileinto", "mailbox", "imap4flags", "copy"}

// buildFilterScript genera el script de las reglas y el reenvio activos; vacio si no hay ninguno.
// Todo va dentro de "no es spam": una regla nunca reenvia ni archiva spam, que sigue su camino hacia
// Junk en global_sieve_after. Los textos del usuario solo entran como cadenas citadas (sieveQuote),
// nunca como codigo; ni variables ni encoded-character se declaran, asi que "${...}" es literal.
func buildFilterScript(f *MailboxFilters) string {
	used := map[string]bool{}
	var body strings.Builder
	if f.Forwarding.Enabled {
		for _, addr := range f.Forwarding.Addresses {
			body.WriteString("  " + redirectAction(addr, f.Forwarding.KeepCopy, used) + "\n")
		}
	}
	for _, r := range f.Rules {
		if !r.Enabled {
			continue
		}
		body.WriteString("  if " + ruleTest(r) + " {\n")
		for _, action := range ruleActions(r, used) {
			body.WriteString("    " + action + "\n")
		}
		body.WriteString("  }\n")
	}
	if body.Len() == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Reglas y reenvio generados por la plataforma; se editan desde el buzon, no a mano.\n")
	var exts []string
	for _, ext := range filterExtensions {
		if used[ext] {
			exts = append(exts, sieveQuote(ext))
		}
	}
	if len(exts) > 0 {
		b.WriteString("require [" + strings.Join(exts, ", ") + "];\n")
	}
	b.WriteString("if not header :contains \"X-Spam-Flag\" \"YES\" {\n")
	b.WriteString(body.String())
	b.WriteString("}\n")
	return b.String()
}

func ruleTest(r FilterRule) string {
	tests := make([]string, 0, len(r.Conditions))
	for _, c := range r.Conditions {
		tests = append(tests, conditionTest(c))
	}
	if len(tests) == 1 {
		return tests[0]
	}
	joiner := "allof"
	if r.Match == FilterMatchAny {
		joiner = "anyof"
	}
	return joiner + " (" + strings.Join(tests, ", ") + ")"
}

// conditionTest: contains mira la cabecera entera (nombre visible incluido); is compara la direccion
// exacta, salvo en el asunto. Las comparaciones no distinguen mayusculas (i;ascii-casemap).
func conditionTest(c FilterCondition) string {
	headers := filterHeaders[c.Field]
	value := sieveQuote(c.Value)
	switch c.Op {
	case FilterOpNotContains:
		return "not header :contains " + headers + " " + value
	case FilterOpIs:
		if c.Field == FilterFieldSubject {
			return "header :is " + headers + " " + value
		}
		return "address :all :is " + headers + " " + value
	default:
		return "header :contains " + headers + " " + value
	}
}

// ruleActions ordena las acciones de una regla: primero las marcas (imap4flags las aplica al fileinto
// y a la entrega que siguen), luego archivar, reenviar, descartar y, al final, stop.
func ruleActions(r FilterRule, used map[string]bool) []string {
	order := []string{FilterActionMarkRead, FilterActionFlag, FilterActionMove, FilterActionForward, FilterActionDiscard}
	var out []string
	for _, kind := range order {
		for _, a := range r.Actions {
			if a.Type != kind {
				continue
			}
			switch kind {
			case FilterActionMarkRead:
				used["imap4flags"] = true
				out = append(out, "addflag "+sieveQuote(`\Seen`)+";")
			case FilterActionFlag:
				used["imap4flags"] = true
				out = append(out, "addflag "+sieveQuote(`\Flagged`)+";")
			case FilterActionMove:
				used["fileinto"], used["mailbox"] = true, true
				out = append(out, "fileinto :create "+sieveQuote(a.Folder)+";")
			case FilterActionForward:
				out = append(out, redirectAction(a.Address, a.KeepCopy, used))
			case FilterActionDiscard:
				out = append(out, "discard;")
			}
		}
	}
	if r.Stop {
		out = append(out, "stop;")
	}
	return out
}

// redirectAction: con copia el mensaje se entrega ademas en el buzon; sin ella, redirect cancela la
// entrega (salvo que otra accion lo archive).
func redirectAction(addr string, keepCopy bool, used map[string]bool) string {
	if keepCopy {
		used["copy"] = true
		return "redirect :copy " + sieveQuote(addr) + ";"
	}
	return "redirect " + sieveQuote(addr) + ";"
}
