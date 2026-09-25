package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Proxy de imagenes remotas: cuando el lector pide ver las imagenes de un mensaje, el HTML no apunta
// al servidor del remitente sino a una URL del propio webmail firmada con HMAC-SHA256. El navegador
// del lector no habla con el remitente (que no ve su IP ni cuando abre el mensaje) y el proxy solo
// descarga lo que el webmail firmo: sin la firma no es un proxy abierto.
//
// La URL no lleva la cookie de sesion (el HTML se pinta en un iframe aislado de origen opaco, que no
// la envia): la firma es la autorizacion. Cubre la URL de la imagen, la caducidad y el buzon que la
// pidio, por el que se cuenta el cupo.

// Errores del proxy de imagenes remotas.
var (
	ErrRemoteImageLinkInvalid = errors.New("enlace de imagen no válido")
	ErrRemoteImageLinkExpired = errors.New("enlace de imagen caducado")
	ErrRemoteImageRefused     = errors.New("la imagen no está en una dirección pública admitida")
	ErrRemoteImageUnavailable = errors.New("no se pudo obtener la imagen")
	ErrRemoteImageTooLarge    = errors.New("la imagen supera el tamaño admitido")
	ErrRemoteImageNotImage    = errors.New("el recurso no es una imagen admitida")
)

// Resultados del proxy para las metricas: un conjunto cerrado.
const (
	RemoteImageOutcomeOK          = "ok"
	RemoteImageOutcomeInvalid     = "invalid"
	RemoteImageOutcomeExpired     = "expired"
	RemoteImageOutcomeRateLimited = "rate_limited"
	RemoteImageOutcomeBusy        = "busy"
	RemoteImageOutcomeRefused     = "refused"
	RemoteImageOutcomeUpstream    = "upstream_error"
	RemoteImageOutcomeTooLarge    = "too_large"
	RemoteImageOutcomeNotImage    = "not_image"
)

// RemoteImageOutcomes son todos los resultados, para inicializar las series a cero.
var RemoteImageOutcomes = []string{
	RemoteImageOutcomeOK, RemoteImageOutcomeInvalid, RemoteImageOutcomeExpired, RemoteImageOutcomeRateLimited,
	RemoteImageOutcomeBusy, RemoteImageOutcomeRefused, RemoteImageOutcomeUpstream, RemoteImageOutcomeTooLarge,
	RemoteImageOutcomeNotImage,
}

const (
	// MaxRemoteImageURLBytes acota la URL que se firma: una mas larga no se muestra.
	MaxRemoteImageURLBytes = 2048
	// remoteImageKeyLabel separa la clave del proxy de cualquier otro uso del secreto del que sale, y
	// remoteImageSignatureLabel encabeza lo firmado. Cambiar el formato exige una etiqueta nueva.
	remoteImageKeyLabel       = "webmail-image-proxy/v1"
	remoteImageSignatureLabel = "image-proxy/v1"
	remoteImageSignatureBytes = sha256.Size
	httpDefaultPort           = "80"
	httpsDefaultPort          = "443"
)

// RemoteImageLink es lo que firma el enlace del proxy.
type RemoteImageLink struct {
	// URL es la de la imagen, ya validada con ValidateRemoteImageURL.
	URL string
	// MailboxID es el buzon (UUID) que leyo el mensaje; su cupo paga la descarga.
	MailboxID string
	// Expires es cuando deja de valer, en segundos exactos.
	Expires time.Time
}

// SignedRemoteImage es el enlace firmado en la forma en que viaja en la URL del proxy: la URL en
// base64url sin relleno, la caducidad en segundos Unix, el buzon y la firma en base64url sin relleno.
type SignedRemoteImage struct {
	EncodedURL string
	Expires    string
	MailboxID  string
	Signature  string
}

// DeriveRemoteImageKey saca del secreto del almacen la clave con la que se firman los enlaces del
// proxy: HMAC-SHA256 del secreto con una etiqueta fija. El secreto no se usa tal cual para firmar.
func DeriveRemoteImageKey(secret []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(remoteImageKeyLabel))
	return mac.Sum(nil)
}

// ValidateRemoteImageURL admite una imagen remota que el proxy puede pedir: http o https en su
// puerto por defecto, con nombre de servidor y sin credenciales. Devuelve la URL sin fragmento (el
// fragmento nunca viaja al servidor). Vale tambien para cada redireccion.
func ValidateRemoteImageURL(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > MaxRemoteImageURLBytes || strings.ContainsAny(raw, " \t\r\n\x00") {
		return nil, ErrRemoteImageRefused
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Hostname() == "" {
		return nil, ErrRemoteImageRefused
	}
	scheme := strings.ToLower(u.Scheme)
	var port string
	switch scheme {
	case "http":
		port = httpDefaultPort
	case "https":
		port = httpsDefaultPort
	default:
		return nil, ErrRemoteImageRefused
	}
	if p := u.Port(); p != "" && p != port {
		return nil, ErrRemoteImageRefused
	}
	u.Scheme = scheme
	u.Fragment = ""
	u.RawFragment = ""
	return u, nil
}

// NewRemoteImageLink prepara el enlace de una imagen del mensaje. La caducidad se redondea hacia
// arriba a un cuarto de ttl: el mismo mensaje abierto varias veces seguidas da la misma URL y el
// navegador reutiliza la imagen ya descargada, y ningun enlace vale menos de ttl.
func NewRemoteImageLink(rawURL, mailboxID string, now time.Time, ttl time.Duration) (RemoteImageLink, error) {
	u, err := ValidateRemoteImageURL(rawURL)
	if err != nil {
		return RemoteImageLink{}, err
	}
	if !ValidUUID(mailboxID) || ttl <= 0 {
		return RemoteImageLink{}, ErrRemoteImageLinkInvalid
	}
	step := max(ttl/4, time.Second)
	expires := now.Add(ttl).Truncate(step).Add(step).Truncate(time.Second)
	return RemoteImageLink{URL: u.String(), MailboxID: strings.ToLower(mailboxID), Expires: expires.UTC()}, nil
}

// Sign firma el enlace con la clave del proxy.
func (l RemoteImageLink) Sign(key []byte) SignedRemoteImage {
	expires := strconv.FormatInt(l.Expires.Unix(), 10)
	return SignedRemoteImage{
		EncodedURL: base64.RawURLEncoding.EncodeToString([]byte(l.URL)),
		Expires:    expires,
		MailboxID:  l.MailboxID,
		Signature:  base64.RawURLEncoding.EncodeToString(remoteImageMAC(key, l.URL, l.MailboxID, expires)),
	}
}

// Verify comprueba la firma y la caducidad y devuelve el enlace. Una firma que no casa, un campo
// ilegible o un buzon cambiado son ErrRemoteImageLinkInvalid; uno vencido, ErrRemoteImageLinkExpired.
// La firma se comprueba antes que la caducidad: un enlace alterado nunca dice si estaria vencido.
func (s SignedRemoteImage) Verify(key []byte, now time.Time) (RemoteImageLink, error) {
	if len(s.EncodedURL) > base64.RawURLEncoding.EncodedLen(MaxRemoteImageURLBytes) || !ValidUUID(s.MailboxID) {
		return RemoteImageLink{}, ErrRemoteImageLinkInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(s.Signature)
	if err != nil || len(sig) != remoteImageSignatureBytes {
		return RemoteImageLink{}, ErrRemoteImageLinkInvalid
	}
	rawURL, err := base64.RawURLEncoding.DecodeString(s.EncodedURL)
	if err != nil {
		return RemoteImageLink{}, ErrRemoteImageLinkInvalid
	}
	expires, err := strconv.ParseInt(s.Expires, 10, 64)
	if err != nil || expires <= 0 || strconv.FormatInt(expires, 10) != s.Expires {
		return RemoteImageLink{}, ErrRemoteImageLinkInvalid
	}
	if !hmac.Equal(sig, remoteImageMAC(key, string(rawURL), s.MailboxID, s.Expires)) {
		return RemoteImageLink{}, ErrRemoteImageLinkInvalid
	}
	at := time.Unix(expires, 0).UTC()
	if !now.Before(at) {
		return RemoteImageLink{}, ErrRemoteImageLinkExpired
	}
	u, err := ValidateRemoteImageURL(string(rawURL))
	if err != nil {
		return RemoteImageLink{}, ErrRemoteImageLinkInvalid
	}
	return RemoteImageLink{URL: u.String(), MailboxID: s.MailboxID, Expires: at}, nil
}

// remoteImageMAC firma la etiqueta, la URL, el buzon y la caducidad separados por saltos de linea:
// ninguno de los tres puede llevar uno (la URL validada no tiene caracteres de control).
func remoteImageMAC(key []byte, rawURL, mailboxID, expires string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(remoteImageSignatureLabel + "\n" + rawURL + "\n" + mailboxID + "\n" + expires))
	return mac.Sum(nil)
}

// RemoteImage es una imagen descargada por el proxy, con el tipo que dicen sus bytes.
type RemoteImage struct {
	ContentType string
	Data        []byte
}

// remoteImageTypes son los mapas de bits que el proxy entrega. SVG queda fuera: es un documento con
// su propio DOM y sus propias referencias.
var remoteImageTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

// SniffRemoteImage decide el tipo de una imagen descargada por su firma de fichero, no por lo que
// declare el servidor remoto: lo que no es uno de los cuatro mapas de bits no se entrega.
func SniffRemoteImage(data []byte) (string, bool) {
	for _, t := range remoteImageTypes {
		if SniffInlineImage(t, data) {
			return t, true
		}
	}
	return "", false
}
