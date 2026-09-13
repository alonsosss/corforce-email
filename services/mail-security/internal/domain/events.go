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
)
