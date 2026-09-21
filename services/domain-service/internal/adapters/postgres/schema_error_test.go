package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestSoloUnEsquemaOUnaColumnaQueFaltanSonUnaEmpresaSinMigrar(t *testing.T) {
	for code, want := range map[string]bool{"3F000": true, "42P01": true, "42703": true, "23505": false, "40001": false, "57014": false} {
		err := schemaNotReady(&pgconn.PgError{Code: code, Message: "m"})
		if got := errors.Is(err, domain.ErrTenantSchemaNotReady); got != want {
			t.Errorf("SQLSTATE %s: sin migrar=%v, se esperaba %v", code, got, want)
		}
	}
	plain := errors.New("conexion perdida")
	if schemaNotReady(plain) != plain {
		t.Fatal("un error que no es de Postgres pasa tal cual")
	}
	wrapped := fmt.Errorf("consulta: %w", &pgconn.PgError{Code: "42703", Message: "column x"})
	if !errors.Is(schemaNotReady(wrapped), domain.ErrTenantSchemaNotReady) {
		t.Fatal("tambien envuelto")
	}
}
