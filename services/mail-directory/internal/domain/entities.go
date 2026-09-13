package domain

import (
	"time"

	"github.com/google/uuid"
)

// Estados tri-estado de buzones y aliases, con la semantica que leen Postfix y Dovecot:
// 0 no recibe ni envia, 1 activo, 2 solo recibe (no puede iniciar sesion ni enviar).
const (
	ActiveOff         = 0
	ActiveOn          = 1
	ActiveReceiveOnly = 2
)

// ActiveState es un valor del tri-estado con un codigo estable para los clientes; el
// texto que ve una persona lo pone la interfaz a partir del codigo.
type ActiveState struct {
	Value int    `json:"value"`
	Code  string `json:"code"`
}

// ActiveStates lista los estados en el orden en que se ofrecen: el habitual primero.
func ActiveStates() []ActiveState {
	return []ActiveState{
		{Value: ActiveOn, Code: "active"},
		{Value: ActiveReceiveOnly, Code: "receive_only"},
		{Value: ActiveOff, Code: "inactive"},
	}
}

// Kinds de buzon: '' para personas; los recursos no reciben correo.
var mailboxKinds = []string{"", "location", "thing", "group"}

func MailboxKinds() []string { return append([]string(nil), mailboxKinds...) }

// Politicas TLS que admite smtp_tls_policy_maps de Postfix (CHECK de la tabla).
var tlsPolicies = []string{"none", "may", "encrypt", "dane", "dane-only", "fingerprint", "verify", "secure"}

func TLSPolicies() []string { return append([]string(nil), tlsPolicies...) }

const (
	BCCTypeSender = "sender"
	BCCTypeRcpt   = "rcpt"

	SieveTypePrefilter  = "prefilter"
	SieveTypePostfilter = "postfilter"
)

func BCCTypes() []string { return []string{BCCTypeSender, BCCTypeRcpt} }

func SieveFilterTypes() []string { return []string{SieveTypePrefilter, SieveTypePostfilter} }

type Domain struct {
	ID                 uuid.UUID  `json:"id"`
	TenantID           uuid.UUID  `json:"tenant_id"`
	Domain             string     `json:"domain"`
	Description        string     `json:"description"`
	Active             bool       `json:"active"`
	BackupMX           bool       `json:"backupmx"`
	RelayAllRecipients bool       `json:"relay_all_recipients"`
	RelayUnknownOnly   bool       `json:"relay_unknown_only"`
	RelayhostID        *uuid.UUID `json:"relayhost_id"`
	MaxAliases         int        `json:"max_aliases"`
	MaxMailboxes       int        `json:"max_mailboxes"`
	DefaultQuotaBytes  int64      `json:"default_quota_bytes"`
	MaxQuotaBytes      int64      `json:"max_quota_bytes"`
	QuotaBytes         int64      `json:"quota_bytes"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type AliasDomain struct {
	ID           uuid.UUID `json:"id"`
	TenantID     uuid.UUID `json:"tenant_id"`
	AliasDomain  string    `json:"alias_domain"`
	TargetDomain string    `json:"target_domain"`
	Active       bool      `json:"active"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Mailbox nunca serializa el hash: la contrasena entra por el API y no vuelve a salir.
type Mailbox struct {
	ID            uuid.UUID  `json:"id"`
	TenantID      uuid.UUID  `json:"tenant_id"`
	Username      string     `json:"username"`
	LocalPart     string     `json:"local_part"`
	Domain        string     `json:"domain"`
	PasswordHash  string     `json:"-"`
	DisplayName   string     `json:"display_name"`
	QuotaBytes    int64      `json:"quota_bytes"`
	Active        int        `json:"active"`
	Kind          string     `json:"kind"`
	TLSEnforceIn  bool       `json:"tls_enforce_in"`
	TLSEnforceOut bool       `json:"tls_enforce_out"`
	RelayhostID   *uuid.UUID `json:"relayhost_id"`
	IMAPAccess    bool       `json:"imap_access"`
	POP3Access    bool       `json:"pop3_access"`
	SMTPAccess    bool       `json:"smtp_access"`
	SieveAccess   bool       `json:"sieve_access"`
	ForcePwUpdate bool       `json:"force_pw_update"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type Alias struct {
	ID             uuid.UUID `json:"id"`
	TenantID       uuid.UUID `json:"tenant_id"`
	Address        string    `json:"address"`
	Goto           string    `json:"goto"`
	Domain         string    `json:"domain"`
	SenderAllowed  bool      `json:"sender_allowed"`
	Internal       bool      `json:"internal"`
	Active         int       `json:"active"`
	PrivateComment string    `json:"private_comment"`
	PublicComment  string    `json:"public_comment"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type SpamAlias struct {
	ID          uuid.UUID  `json:"id"`
	TenantID    uuid.UUID  `json:"tenant_id"`
	Address     string     `json:"address"`
	Goto        string     `json:"goto"`
	Description string     `json:"description"`
	ValidUntil  *time.Time `json:"valid_until"`
	Permanent   bool       `json:"permanent"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type SenderACL struct {
	ID         uuid.UUID `json:"id"`
	TenantID   uuid.UUID `json:"tenant_id"`
	LoggedInAs string    `json:"logged_in_as"`
	SendAs     string    `json:"send_as"`
	External   bool      `json:"external"`
	CreatedAt  time.Time `json:"created_at"`
}

type AppPassword struct {
	ID           uuid.UUID  `json:"id"`
	TenantID     uuid.UUID  `json:"tenant_id"`
	MailboxID    uuid.UUID  `json:"mailbox_id"`
	Name         string     `json:"name"`
	PasswordHash string     `json:"-"`
	IMAPAccess   bool       `json:"imap_access"`
	POP3Access   bool       `json:"pop3_access"`
	SMTPAccess   bool       `json:"smtp_access"`
	SieveAccess  bool       `json:"sieve_access"`
	DAVAccess    bool       `json:"dav_access"`
	Active       bool       `json:"active"`
	LastUsedAt   *time.Time `json:"last_used_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// Relayhost y Transport llevan la contrasena SASL en claro en la base porque Postfix la
// lee por un mapa pgsql. Aqui solo se sabe si hay una: la columna no se lee nunca.
type Relayhost struct {
	ID          uuid.UUID `json:"id"`
	TenantID    uuid.UUID `json:"tenant_id"`
	Hostname    string    `json:"hostname"`
	Username    string    `json:"username"`
	HasPassword bool      `json:"has_password"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Transport con TenantID nil es una ruta de plataforma: la ven todas las empresas y solo
// la administra quien opera la plataforma.
type Transport struct {
	ID          uuid.UUID  `json:"id"`
	TenantID    *uuid.UUID `json:"tenant_id"`
	Destination string     `json:"destination"`
	Nexthop     string     `json:"nexthop"`
	Username    string     `json:"username"`
	HasPassword bool       `json:"has_password"`
	IsMXBased   bool       `json:"is_mx_based"`
	Active      bool       `json:"active"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (t Transport) IsPlatform() bool { return t.TenantID == nil }

type TLSPolicy struct {
	ID         uuid.UUID `json:"id"`
	TenantID   uuid.UUID `json:"tenant_id"`
	Dest       string    `json:"dest"`
	Policy     string    `json:"policy"`
	Parameters string    `json:"parameters"`
	Active     bool      `json:"active"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type RecipientMap struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	OldDest   string    `json:"old_dest"`
	NewDest   string    `json:"new_dest"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type BCCMap struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	LocalDest string    `json:"local_dest"`
	BCCDest   string    `json:"bcc_dest"`
	Domain    string    `json:"domain"`
	Type      string    `json:"type"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SieveFilter es el prefiltro o postfiltro de un buzon. Active se traduce a script_name
// ('active'/'inactive'), que es lo que Dovecot lee por las vistas v_sieve_*.
type SieveFilter struct {
	ID         uuid.UUID `json:"id"`
	TenantID   uuid.UUID `json:"tenant_id"`
	Username   string    `json:"username"`
	FilterType string    `json:"filter_type"`
	ScriptDesc string    `json:"script_desc"`
	ScriptData string    `json:"script_data"`
	Active     bool      `json:"active"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type MailboxSieve struct {
	Prefilter  *SieveFilter `json:"prefilter"`
	Postfilter *SieveFilter `json:"postfilter"`
}

type QuotaUsage struct {
	QuotaBytes int64 `json:"quota_bytes"`
	UsedBytes  int64 `json:"used_bytes"`
	Messages   int64 `json:"messages"`
}

type SASLLogin struct {
	ID            uuid.UUID  `json:"id"`
	Username      string     `json:"username"`
	Service       string     `json:"service"`
	AppPasswordID *uuid.UUID `json:"app_password_id"`
	RemoteIP      string     `json:"remote_ip"`
	LoggedAt      time.Time  `json:"logged_at"`
}
