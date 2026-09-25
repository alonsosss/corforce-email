package postgres

import "github.com/alonsosss/corforce-email/pkg/keyrotation"

// SealedColumns son las columnas que domain-service guarda cifradas con MAIL_ENCRYPTION_KEY en la base
// de cada empresa, para re-cifrarlas bajo la llave activa al rotarla: la clave privada DKIM vigente,
// la anterior mientras dura la gracia de una rotacion y el token de API de cada proveedor DNS. Las tres
// se cifran sin datos autenticados (ports.Cipher: Encrypt y Decrypt). Una columna cifrada nueva se
// anade aqui; si no, retirar la llave vieja la dejaria ilegible.
func SealedColumns() []keyrotation.TenantTarget {
	return []keyrotation.TenantTarget{
		{Column: keyrotation.MustColumn(nil, "domains.domains", "id", "dkim_private_key_enc")},
		{Column: keyrotation.MustColumn(nil, "domains.domains", "id", "dkim_previous_private_key_enc")},
		{Column: keyrotation.MustColumn(nil, "domains.dns_providers", "id", "api_token_enc")},
	}
}
