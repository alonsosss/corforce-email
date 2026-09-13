package domain

import (
	"net/mail"
	"strings"
)

// NoticeOwnNoticeCode es el motivo con que se dan por atendidos los avisos de cuarentena
// que vuelven a entrar y acaban retenidos.
const NoticeOwnNoticeCode = "OWN_NOTICE"

// SplitOwnNotices separa los mensajes retenidos cuyo remitente es el propio remitente del
// aviso. El aviso sale por SES hacia un buzon de la empresa y vuelve a pasar por los
// motores: si acaba en cuarentena, avisar de el generaria otro aviso, y asi sin fin. Un
// remitente vacio o que no es una direccion nunca cuenta como propio.
func SplitOwnNotices(items []QuarantineItem, noticeSender string) (own, rest []QuarantineItem) {
	target := normalizeSenderAddress(noticeSender)
	for _, it := range items {
		if target != "" && normalizeSenderAddress(it.Sender) == target {
			own = append(own, it)
			continue
		}
		rest = append(rest, it)
	}
	return own, rest
}

// normalizeSenderAddress reduce un remitente a su direccion en minusculas. Admite
// "Nombre <a@b>", "<a@b>" y "a@b"; lo que no es una direccion devuelve "".
func normalizeSenderAddress(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || s == "<>" {
		return ""
	}
	if addr, err := mail.ParseAddress(s); err == nil {
		s = addr.Address
	} else {
		s = strings.Trim(s, "<>")
	}
	if _, _, ok := SplitAddress(s); !ok {
		return ""
	}
	return strings.ToLower(s)
}
