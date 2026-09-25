package domain

// Eventos que publica este servicio (stream MAIL_SECURITY) y los que consume del
// directorio (stream MAIL_DIRECTORY, lo declara mail-directory).
const (
	StreamName = "MAIL_SECURITY"

	SubjectQuarantineStored   = "mail_security.quarantine.stored"
	SubjectQuarantineReleased = "mail_security.quarantine.released"

	// Consumidor durable de los eventos del directorio que alteran las claves de Redis.
	// DirectoryStreamName y DirectorySubjectPattern repiten la declaracion de
	// mail-directory: el consumidor declara su stream (la union de subjects es idempotente).
	DirectoryStreamName     = "MAIL_DIRECTORY"
	DirectorySubjectPattern = "mail.>"
	DirectoryConsumer       = "mail-security-redis"

	// Consumidor durable aparte de los eventos de buzon que revocan credenciales en Dovecot: un
	// Dovecot caido reentrega solo estos y no retiene los de Redis.
	MailboxSubjectPattern            = "mail.mailbox.>"
	SessionsConsumer                 = "mail-security-dovecot"
	SubjectMailboxUpdated            = "mail.mailbox.updated"
	SubjectMailboxDeleted            = "mail.mailbox.deleted"
	SubjectMailboxCredentialsChanged = "mail.mailbox.credentials_changed"
	// SubjectMailboxMFAEnabled: el buzon activo la verificacion en dos pasos y su contrasena principal
	// deja de abrir IMAP, POP3, SMTP y Sieve (mail-auth). Se trata como credencial cambiada: la cache
	// de Dovecot y las sesiones abiertas con esa contrasena seguirian valiendo.
	SubjectMailboxMFAEnabled = "mail.mailbox.mfa_enabled"
)
