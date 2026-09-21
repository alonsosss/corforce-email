package app

import "github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"

// Meta son las reglas del directorio que un cliente necesita para validar y ofrecer
// opciones antes de enviar. Todo sale de las constantes del dominio y de la paginacion de
// este paquete: la interfaz deja de copiarlas y no puede quedarse desfasada.
type Meta struct {
	Mailbox     MailboxMeta    `json:"mailbox"`
	Alias       AliasMeta      `json:"alias"`
	Domain      DomainMeta     `json:"domain"`
	Sieve       SieveMeta      `json:"sieve"`
	TLSPolicies []string       `json:"tls_policies"`
	BCCMapTypes []string       `json:"bcc_map_types"`
	Limits      LimitsMeta     `json:"limits"`
	Pagination  PaginationMeta `json:"pagination"`
	Search      SearchMeta     `json:"search"`
	// DAV es null mientras el operador no publique la URL de mail-dav (MAIL_DAV_PUBLIC_URL): la
	// interfaz no ofrece datos de conexion que no existen.
	DAV *DAVMeta `json:"dav"`
}

// DAVMeta son los datos que un cliente de contactos necesita para conectarse a mail-dav.
type DAVMeta struct {
	ServerURL string `json:"server_url"`
}

type MailboxMeta struct {
	PasswordMinLength    int                  `json:"password_min_length"`
	PasswordMaxLength    int                  `json:"password_max_length"`
	LocalPartMaxLength   int                  `json:"local_part_max_length"`
	DisplayNameMaxLength int                  `json:"display_name_max_length"`
	Kinds                []string             `json:"kinds"`
	ActiveStates         []domain.ActiveState `json:"active_states"`
}

type AliasMeta struct {
	ActiveStates []domain.ActiveState `json:"active_states"`
}

type DomainMeta struct {
	NameMaxLength        int `json:"name_max_length"`
	LabelMaxLength       int `json:"label_max_length"`
	DescriptionMaxLength int `json:"description_max_length"`
}

type SieveMeta struct {
	ScriptMaxBytes int      `json:"script_max_bytes"`
	FilterTypes    []string `json:"filter_types"`
}

// LimitsMeta: las cuotas viajan en QuotaUnit y Unlimited significa "sin limite" tanto en
// cuotas como en los maximos de buzones y aliases.
type LimitsMeta struct {
	QuotaUnit string `json:"quota_unit"`
	Unlimited int    `json:"unlimited"`
}

type PaginationMeta struct {
	DefaultPageSize int `json:"default_page_size"`
	MaxPageSize     int `json:"max_page_size"`
}

type SearchMeta struct {
	MaxLength int `json:"max_length"`
}

// Meta son las reglas del directorio mas lo que el despliegue configura (hoy la URL de mail-dav).
func (uc *UseCase) Meta() Meta {
	m := DirectoryMeta()
	if uc.davServerURL != "" {
		m.DAV = &DAVMeta{ServerURL: uc.davServerURL}
	}
	return m
}

// DirectoryMeta no depende de la empresa ni de la base: son reglas del servicio.
func DirectoryMeta() Meta {
	return Meta{
		Mailbox: MailboxMeta{
			PasswordMinLength:    domain.MinPasswordLength,
			PasswordMaxLength:    domain.MaxPasswordLength,
			LocalPartMaxLength:   domain.MaxLocalPartLength,
			DisplayNameMaxLength: domain.MaxDisplayNameLength,
			Kinds:                domain.MailboxKinds(),
			ActiveStates:         domain.ActiveStates(),
		},
		Alias: AliasMeta{ActiveStates: domain.ActiveStates()},
		Domain: DomainMeta{
			NameMaxLength:        domain.MaxDomainLength,
			LabelMaxLength:       domain.MaxLabelLength,
			DescriptionMaxLength: domain.MaxDescriptionLength,
		},
		Sieve:       SieveMeta{ScriptMaxBytes: domain.MaxSieveScriptBytes, FilterTypes: domain.SieveFilterTypes()},
		TLSPolicies: domain.TLSPolicies(),
		BCCMapTypes: domain.BCCTypes(),
		Limits:      LimitsMeta{QuotaUnit: domain.QuotaUnit, Unlimited: domain.Unlimited},
		Pagination:  PaginationMeta{DefaultPageSize: defaultPageSize, MaxPageSize: maxPageSize},
		Search:      SearchMeta{MaxLength: domain.MaxSearchLength},
	}
}
