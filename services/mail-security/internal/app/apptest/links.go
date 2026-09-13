package apptest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

// LegacyLinkSignature firma un enlace de cuarentena con la forma sin celda, la de los
// enlaces emitidos antes de llevarla en la ruta. El servicio ya no la emite: se escribe aqui
// a mano para fijar el formato de los enlaces que ya se enviaron.
func LegacyLinkSignature(key string, c domain.QuarantineLinkClaims) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte("quarantine-link\n" + c.TenantID.String() + "\n" + c.MessageID.String() + "\n" +
		string(c.Action) + "\n" + strconv.FormatInt(c.ExpiresAt, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}
