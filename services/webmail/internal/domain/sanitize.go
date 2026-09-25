package domain

// SanitizeOptions gobierna el saneado del HTML de un mensaje recibido.
type SanitizeOptions struct {
	// AllowRemoteImages deja cargar imagenes de terceros. Por defecto no: una imagen
	// remota confirma al remitente que el mensaje se abrio, cuando y desde donde.
	AllowRemoteImages bool
	// ProxyRemoteImage devuelve la URL del proxy de imagenes con la que se sirve una imagen
	// remota permitida; ok es false si esa imagen no se puede servir. Una imagen remota nunca
	// sale con su URL original: sin ProxyRemoteImage se quita aunque AllowRemoteImages lo
	// permita, y el navegador del lector no habla nunca con el servidor del remitente.
	ProxyRemoteImage func(src string) (string, bool)
	// ResolveCID devuelve la URL con la que el cliente pide la parte con ese Content-ID.
	ResolveCID func(contentID string) (string, bool)
}

// SanitizedHTML es el resultado del saneado: el HTML seguro, si llevaba imagenes remotas y si
// alguna se sirve por el proxy.
type SanitizedHTML struct {
	HTML         string
	RemoteImages bool
	Proxied      bool
}
