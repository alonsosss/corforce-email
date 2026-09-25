package postgres

import (
	"github.com/alonsosss/corforce-email/pkg/keyrotation"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
)

// SealedColumns son las columnas que mail-migration guarda cifradas con MAIL_ENCRYPTION_KEY en la base
// de cada empresa, para re-cifrarlas bajo la llave activa al rotarla: la contrasena de origen de cada
// trabajo activo, con los datos autenticados de su empresa y su trabajo (domain.SourcePasswordAAD). Una
// columna cifrada nueva se anade aqui; si no, retirar la llave vieja la dejaria ilegible.
func SealedColumns() []keyrotation.TenantTarget {
	return []keyrotation.TenantTarget{
		{Column: keyrotation.MustColumn(nil, "mail_migration.jobs", "id", "source_password_enc"), AAD: domain.SourcePasswordAAD},
	}
}
