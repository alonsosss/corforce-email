package domain

// Credential dice que credencial de un buzon anuncia mail.mailbox.credentials_changed. Quien guarda
// sesiones por credencial decide con ella: el webmail solo admite la principal y no cierra las suyas
// por una de aplicacion; mail-security echa al buzon de Dovecot con cualquiera, porque una sesion no
// dice con que credencial entro.
type Credential string

const (
	CredentialPassword    Credential = "password"
	CredentialAppPassword Credential = "app_password"
)

func (c Credential) Valid() bool {
	return c == CredentialPassword || c == CredentialAppPassword
}

// appPasswordLogins son los inicios de sesion que abre una contrasena de aplicacion: los protocolos
// que mail-auth le comprueba (imap, pop3, smtp y sieve) mientras esta activa. dav_access no abre nada
// en la plataforma, que no sirve DAV.
func (p AppPassword) appPasswordLogins() [4]bool {
	if !p.Active {
		return [4]bool{}
	}
	return [4]bool{p.IMAPAccess, p.POP3Access, p.SMTPAccess, p.SieveAccess}
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
	for i, had := range before.appPasswordLogins() {
		if had && !now[i] {
			return true
		}
	}
	return false
}
