package domain

import "github.com/google/uuid"

// MailboxAttr nombra lo que un hecho del buzon cambio, con el mismo nombre que lleva en el JSON
// del buzon y en el API. Los eventos mail.mailbox.updated y mail.mailbox.credentials_changed lo
// publican en changed: quien guarda sesiones del buzon decide con esa lista si tiene que cerrarlas
// en vez de cerrarlas ante cualquier cambio. AttrPassword y AttrAppPassword no son columnas del
// buzon: nombran la credencial que dejo de valer.
type MailboxAttr string

const (
	AttrDisplayName   MailboxAttr = "display_name"
	AttrQuotaBytes    MailboxAttr = "quota_bytes"
	AttrActive        MailboxAttr = "active"
	AttrKind          MailboxAttr = "kind"
	AttrTLSEnforceIn  MailboxAttr = "tls_enforce_in"
	AttrTLSEnforceOut MailboxAttr = "tls_enforce_out"
	AttrRelayhostID   MailboxAttr = "relayhost_id"
	AttrIMAPAccess    MailboxAttr = "imap_access"
	AttrPOP3Access    MailboxAttr = "pop3_access"
	AttrSMTPAccess    MailboxAttr = "smtp_access"
	AttrSieveAccess   MailboxAttr = "sieve_access"
	AttrDAVAccess     MailboxAttr = "dav_access"
	AttrForcePwUpdate MailboxAttr = "force_pw_update"
	AttrPassword      MailboxAttr = "password"
	AttrAppPassword   MailboxAttr = "app_password"
)

var mailboxAttrs = map[MailboxAttr]bool{
	AttrDisplayName: true, AttrQuotaBytes: true, AttrActive: true, AttrKind: true,
	AttrTLSEnforceIn: true, AttrTLSEnforceOut: true, AttrRelayhostID: true,
	AttrIMAPAccess: true, AttrPOP3Access: true, AttrSMTPAccess: true, AttrSieveAccess: true, AttrDAVAccess: true,
	AttrForcePwUpdate: true, AttrPassword: true, AttrAppPassword: true,
}

func (a MailboxAttr) Valid() bool { return mailboxAttrs[a] }

// MailboxChanges son los atributos en los que before y after difieren, siempre en este orden.
// Una peticion que repite lo que el buzon ya tenia no cambia ninguno y devuelve la lista vacia:
// el evento sale igual, diciendo que no cambio nada.
func MailboxChanges(before, after Mailbox) []MailboxAttr {
	var out []MailboxAttr
	add := func(changed bool, a MailboxAttr) {
		if changed {
			out = append(out, a)
		}
	}
	add(before.DisplayName != after.DisplayName, AttrDisplayName)
	add(before.QuotaBytes != after.QuotaBytes, AttrQuotaBytes)
	add(before.Active != after.Active, AttrActive)
	add(before.Kind != after.Kind, AttrKind)
	add(before.TLSEnforceIn != after.TLSEnforceIn, AttrTLSEnforceIn)
	add(before.TLSEnforceOut != after.TLSEnforceOut, AttrTLSEnforceOut)
	add(!sameRelayhost(before.RelayhostID, after.RelayhostID), AttrRelayhostID)
	add(before.IMAPAccess != after.IMAPAccess, AttrIMAPAccess)
	add(before.POP3Access != after.POP3Access, AttrPOP3Access)
	add(before.SMTPAccess != after.SMTPAccess, AttrSMTPAccess)
	add(before.SieveAccess != after.SieveAccess, AttrSieveAccess)
	add(before.DAVAccess != after.DAVAccess, AttrDAVAccess)
	add(before.ForcePwUpdate != after.ForcePwUpdate, AttrForcePwUpdate)
	return out
}

func sameRelayhost(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
