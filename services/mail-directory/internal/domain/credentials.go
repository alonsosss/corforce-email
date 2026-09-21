package domain

// Credential dice que credencial de un buzon anuncia mail.mailbox.credentials_changed. Quien guarda
// sesiones por credencial decide con ella: el webmail solo admite la principal y no cierra las suyas
// por una de aplicacion; mail-security echa al buzon de Dovecot con cualquiera, porque una sesion no
// dice con que credencial entro. Un cambio del propio buzon que le quita inicios de sesion
// (MailboxLoginsRevoked) los quita a todas sus credenciales y sale como la principal.
type Credential string

const (
	CredentialPassword    Credential = "password"
	CredentialAppPassword Credential = "app_password"
)

func (c Credential) Valid() bool {
	return c == CredentialPassword || c == CredentialAppPassword
}

// appPasswordLogins son los inicios de sesion que abre una contrasena de aplicacion: los protocolos
// que mail-auth le comprueba (imap, pop3, smtp y sieve) mientras esta activa. dav_access queda fuera: mail-dav
// autentica cada peticion contra mail-auth sin sesion ni cache, asi que perderlo no deja nada que cerrar.
func (p AppPassword) appPasswordLogins() [4]bool {
	if !p.Active {
		return [4]bool{}
	}
	return [4]bool{p.IMAPAccess, p.POP3Access, p.SMTPAccess, p.SieveAccess}
}

// mailboxLogins son los inicios de sesion que abre el buzon con cualquiera de sus credenciales:
// mail-auth exige active 1 y el flag del protocolo tanto a la contrasena principal como a las de
// aplicacion, y el webmail necesita imap y smtp. dav_access tampoco cuenta, por lo mismo que en la de
// aplicacion.
func (m Mailbox) mailboxLogins() [4]bool {
	if m.Active != ActiveOn {
		return [4]bool{}
	}
	return [4]bool{m.IMAPAccess, m.POP3Access, m.SMTPAccess, m.SieveAccess}
}

// AppPasswordLoginsRevoked dice si pasar de before a after (nil si se borra) le quita a la
// contrasena de aplicacion algun inicio de sesion que tenia: solo entonces puede quedar en la cache
// de Dovecot una autenticacion que ya no vale y abierta una sesion que ya no deberia estarlo. Darla de
// alta, reactivarla, ampliar sus protocolos o renombrarla no deja ninguna credencial vieja valiendo.
func AppPasswordLoginsRevoked(before AppPassword, after *AppPassword) bool {
	var now [4]bool
	if after != nil {
		now = after.appPasswordLogins()
	}
	return loginsLost(before.appPasswordLogins(), now)
}

// MailboxLoginsRevoked es la misma regla para el buzon: apagarlo, dejarlo solo en recepcion o
// quitarle imap_access, pop3_access, smtp_access o sieve_access le quita un inicio de sesion a todas
// sus credenciales, y una sesion ya abierta con ese protocolo seguiria abierta. Reactivarlo, darle
// protocolos o cambiar lo demas (cuota, nombre, TLS, relayhost) no retira nada.
func MailboxLoginsRevoked(before, after Mailbox) bool {
	return loginsLost(before.mailboxLogins(), after.mailboxLogins())
}

func loginsLost(before, after [4]bool) bool {
	for i, had := range before {
		if had && !after[i] {
			return true
		}
	}
	return false
}
