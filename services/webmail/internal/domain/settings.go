package domain

import "time"

// Signature es la firma del buzon. La guarda mail-directory con el HTML ya saneado por el
// webmail y su version en texto; el tope lo sirve el directorio.
type Signature struct {
	Enabled   bool
	HTML      string
	Text      string
	OnReplies bool
	UpdatedAt *time.Time
	Limits    SignatureLimits
}

type SignatureLimits struct {
	MaxHTMLBytes int
	MaxTextBytes int
}

// SignatureInput es lo que se guarda: HTML saneado y su texto plano.
type SignatureInput struct {
	Enabled   bool
	HTML      string
	Text      string
	OnReplies bool
}

// FilterCondition, FilterAction y FilterRule son una regla de correo tal como la valida
// mail-directory, que genera con ella el script Sieve. El webmail no copia la regla: la transporta.
type FilterCondition struct {
	Field string
	Op    string
	Value string
}

// FilterAction lleva solo los datos de su tipo: Folder en move; Address y KeepCopy en forward.
type FilterAction struct {
	Type     string
	Folder   string
	Address  string
	KeepCopy *bool
}

type FilterRule struct {
	ID         string
	Name       string
	Enabled    bool
	Match      string
	Conditions []FilterCondition
	Actions    []FilterAction
	Stop       bool
}

// Forwarding es el reenvio de todo el correo del buzon.
type Forwarding struct {
	Enabled   bool
	Addresses []string
	KeepCopy  bool
}

// MailFilters son las reglas y el reenvio del buzon. Limits son los topes que sirve
// mail-directory, por nombre, sin copia en el webmail.
type MailFilters struct {
	Rules      []FilterRule
	Forwarding Forwarding
	UpdatedAt  *time.Time
	Limits     map[string]int
}

// MailFiltersInput reemplaza las reglas y el reenvio enteros.
type MailFiltersInput struct {
	Rules      []FilterRule
	Forwarding Forwarding
	// Reauthenticated dice a mail-directory que el usuario acaba de confirmar su contrasena (y su
	// codigo si tiene verificacion en dos pasos). Solo lo fija el caso de uso tras comprobarlas.
	Reauthenticated bool
}
