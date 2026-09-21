package http

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// nullableUUID distingue en un PATCH entre "no viene" (Set = false), "null" (Set = true,
// Value = nil: desvincular) y un valor. Un puntero solo no diferencia los dos primeros.
type nullableUUID struct {
	Set   bool
	Value *uuid.UUID
}

func (n *nullableUUID) UnmarshalJSON(data []byte) error {
	n.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		n.Value = nil
		return nil
	}
	var id uuid.UUID
	if err := json.Unmarshal(data, &id); err != nil {
		return err
	}
	n.Value = &id
	return nil
}

type createDomainRequest struct {
	Domain             string     `json:"domain"`
	Description        string     `json:"description"`
	BackupMX           bool       `json:"backupmx"`
	RelayAllRecipients bool       `json:"relay_all_recipients"`
	RelayUnknownOnly   bool       `json:"relay_unknown_only"`
	RelayhostID        *uuid.UUID `json:"relayhost_id"`
	MaxAliases         int        `json:"max_aliases"`
	MaxMailboxes       int        `json:"max_mailboxes"`
	DefaultQuotaBytes  int64      `json:"default_quota_bytes"`
	MaxQuotaBytes      int64      `json:"max_quota_bytes"`
	QuotaBytes         int64      `json:"quota_bytes"`
}

type updateDomainRequest struct {
	Description        *string      `json:"description"`
	Active             *bool        `json:"active"`
	BackupMX           *bool        `json:"backupmx"`
	RelayAllRecipients *bool        `json:"relay_all_recipients"`
	RelayUnknownOnly   *bool        `json:"relay_unknown_only"`
	RelayhostID        nullableUUID `json:"relayhost_id"`
	MaxAliases         *int         `json:"max_aliases"`
	MaxMailboxes       *int         `json:"max_mailboxes"`
	DefaultQuotaBytes  *int64       `json:"default_quota_bytes"`
	MaxQuotaBytes      *int64       `json:"max_quota_bytes"`
	QuotaBytes         *int64       `json:"quota_bytes"`
}

type activationRequest struct {
	Active *bool `json:"active"`
}

type createAliasDomainRequest struct {
	AliasDomain  string `json:"alias_domain"`
	TargetDomain string `json:"target_domain"`
	Active       *bool  `json:"active"`
}

type updateAliasDomainRequest struct {
	TargetDomain *string `json:"target_domain"`
	Active       *bool   `json:"active"`
}

type createMailboxRequest struct {
	LocalPart     string     `json:"local_part"`
	Domain        string     `json:"domain"`
	Password      string     `json:"password"`
	DisplayName   string     `json:"display_name"`
	QuotaBytes    *int64     `json:"quota_bytes"`
	Active        *int       `json:"active"`
	Kind          string     `json:"kind"`
	TLSEnforceIn  bool       `json:"tls_enforce_in"`
	TLSEnforceOut bool       `json:"tls_enforce_out"`
	IMAPAccess    *bool      `json:"imap_access"`
	POP3Access    *bool      `json:"pop3_access"`
	SMTPAccess    *bool      `json:"smtp_access"`
	SieveAccess   *bool      `json:"sieve_access"`
	DAVAccess     *bool      `json:"dav_access"`
	ForcePwUpdate bool       `json:"force_pw_update"`
	RelayhostID   *uuid.UUID `json:"relayhost_id"`
}

type updateMailboxRequest struct {
	DisplayName   *string      `json:"display_name"`
	QuotaBytes    *int64       `json:"quota_bytes"`
	Active        *int         `json:"active"`
	Kind          *string      `json:"kind"`
	TLSEnforceIn  *bool        `json:"tls_enforce_in"`
	TLSEnforceOut *bool        `json:"tls_enforce_out"`
	IMAPAccess    *bool        `json:"imap_access"`
	POP3Access    *bool        `json:"pop3_access"`
	SMTPAccess    *bool        `json:"smtp_access"`
	SieveAccess   *bool        `json:"sieve_access"`
	DAVAccess     *bool        `json:"dav_access"`
	ForcePwUpdate *bool        `json:"force_pw_update"`
	RelayhostID   nullableUUID `json:"relayhost_id"`
}

type setPasswordRequest struct {
	Password string `json:"password"`
}

type createAppPasswordRequest struct {
	Name        string `json:"name"`
	IMAPAccess  *bool  `json:"imap_access"`
	POP3Access  *bool  `json:"pop3_access"`
	SMTPAccess  *bool  `json:"smtp_access"`
	SieveAccess *bool  `json:"sieve_access"`
	DAVAccess   *bool  `json:"dav_access"`
}

type updateAppPasswordRequest struct {
	Name        *string `json:"name"`
	IMAPAccess  *bool   `json:"imap_access"`
	POP3Access  *bool   `json:"pop3_access"`
	SMTPAccess  *bool   `json:"smtp_access"`
	SieveAccess *bool   `json:"sieve_access"`
	DAVAccess   *bool   `json:"dav_access"`
	Active      *bool   `json:"active"`
}

type sieveScriptRequest struct {
	ScriptDesc string `json:"script_desc"`
	ScriptData string `json:"script_data"`
	Active     bool   `json:"active"`
}

type putSieveRequest struct {
	Prefilter  *sieveScriptRequest `json:"prefilter"`
	Postfilter *sieveScriptRequest `json:"postfilter"`
}

type createAliasRequest struct {
	Address        string `json:"address"`
	Goto           string `json:"goto"`
	SenderAllowed  *bool  `json:"sender_allowed"`
	Internal       bool   `json:"internal"`
	Active         *int   `json:"active"`
	PrivateComment string `json:"private_comment"`
	PublicComment  string `json:"public_comment"`
}

type updateAliasRequest struct {
	Goto           *string `json:"goto"`
	SenderAllowed  *bool   `json:"sender_allowed"`
	Internal       *bool   `json:"internal"`
	Active         *int    `json:"active"`
	PrivateComment *string `json:"private_comment"`
	PublicComment  *string `json:"public_comment"`
}

type createSpamAliasRequest struct {
	Address     string     `json:"address"`
	Goto        string     `json:"goto"`
	Description string     `json:"description"`
	ValidUntil  *time.Time `json:"valid_until"`
	Permanent   bool       `json:"permanent"`
}

type updateSpamAliasRequest struct {
	Description *string    `json:"description"`
	ValidUntil  *time.Time `json:"valid_until"`
	Permanent   *bool      `json:"permanent"`
}

type createSenderACLRequest struct {
	LoggedInAs string `json:"logged_in_as"`
	SendAs     string `json:"send_as"`
	External   bool   `json:"external"`
}

type updateSenderACLRequest struct {
	SendAs   *string `json:"send_as"`
	External *bool   `json:"external"`
}

type createRelayhostRequest struct {
	Hostname string `json:"hostname"`
	Username string `json:"username"`
	Password string `json:"password"`
	Active   *bool  `json:"active"`
}

type updateRelayhostRequest struct {
	Hostname *string `json:"hostname"`
	Username *string `json:"username"`
	Password *string `json:"password"`
	Active   *bool   `json:"active"`
}

type createTransportRequest struct {
	Destination string `json:"destination"`
	Nexthop     string `json:"nexthop"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	IsMXBased   bool   `json:"is_mx_based"`
	Active      *bool  `json:"active"`
	Platform    bool   `json:"platform"`
}

type updateTransportRequest struct {
	Destination *string `json:"destination"`
	Nexthop     *string `json:"nexthop"`
	Username    *string `json:"username"`
	Password    *string `json:"password"`
	IsMXBased   *bool   `json:"is_mx_based"`
	Active      *bool   `json:"active"`
}

type createTLSPolicyRequest struct {
	Dest       string `json:"dest"`
	Policy     string `json:"policy"`
	Parameters string `json:"parameters"`
	Active     *bool  `json:"active"`
}

type updateTLSPolicyRequest struct {
	Policy     *string `json:"policy"`
	Parameters *string `json:"parameters"`
	Active     *bool   `json:"active"`
}

type createRecipientMapRequest struct {
	OldDest string `json:"old_dest"`
	NewDest string `json:"new_dest"`
	Active  *bool  `json:"active"`
}

type updateRecipientMapRequest struct {
	NewDest *string `json:"new_dest"`
	Active  *bool   `json:"active"`
}

type createBCCMapRequest struct {
	LocalDest string `json:"local_dest"`
	BCCDest   string `json:"bcc_dest"`
	Type      string `json:"type"`
	Active    *bool  `json:"active"`
}

type updateBCCMapRequest struct {
	BCCDest *string `json:"bcc_dest"`
	Type    *string `json:"type"`
	Active  *bool   `json:"active"`
}
