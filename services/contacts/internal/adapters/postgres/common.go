// Package postgres implementa los puertos sobre la base de la empresa (esquema
// contacts). El pool o la transaccion salen del contexto (db.ContextPool).
package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// uniqueViolation es el SQLSTATE de un choque con una restriccion UNIQUE.
const uniqueViolation = "23505"

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}

// escapeLike neutraliza los comodines de LIKE en el texto del usuario.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// jsonObject serializa un mapa como objeto JSON; nil se guarda como {} (las columnas
// jsonb del esquema exigen objeto).
func jsonObject(m map[string]any) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// readObject lee un objeto jsonb conservando los numeros como json.Number: un atributo
// numerico no pasa por float64 ni pierde precision.
func readObject(b []byte) (map[string]any, error) {
	out := map[string]any{}
	if len(b) == 0 {
		return out, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("leer objeto jsonb: %w", err)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
