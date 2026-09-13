package domain

// SanitizeOptions gobierna el saneado del HTML de un mensaje recibido.
type SanitizeOptions struct {
	// AllowRemoteImages deja cargar imagenes de terceros. Por defecto no: una imagen
	// remota confirma al remitente que el mensaje se abrio, cuando y desde donde.
	AllowRemoteImages bool
	// ResolveCID devuelve la URL con la que el cliente pide la parte con ese Content-ID.
	ResolveCID func(contentID string) (string, bool)
}

// SanitizedHTML es el resultado del saneado: el HTML seguro y si llevaba imagenes remotas.
type SanitizedHTML struct {
	HTML         string
	RemoteImages bool
}
