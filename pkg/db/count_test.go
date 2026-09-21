package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type filaDePrueba struct {
	n   int64
	err error
}

func (f filaDePrueba) Scan(dest ...any) error {
	if f.err != nil {
		return f.err
	}
	*(dest[0].(*int64)) = f.n
	return nil
}

type consultorDePrueba struct {
	fila filaDePrueba
	sql  string
	args []any
}

func (c *consultorDePrueba) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	c.sql, c.args = sql, args
	return c.fila
}

func TestCountCappedExactoHastaElTope(t *testing.T) {
	for _, tc := range []struct {
		nombre string
		filas  int64
		total  int64
		capped bool
	}{
		{"sin filas", 0, 0, false},
		{"por debajo", 42, 42, false},
		{"justo en el tope", 100, 100, false},
		{"un registro por encima", 101, 100, true},
	} {
		t.Run(tc.nombre, func(t *testing.T) {
			q := &consultorDePrueba{fila: filaDePrueba{n: tc.filas}}
			total, capped, err := CountCapped(context.Background(), q, "FROM t WHERE a = $1", []any{"x"}, 100)
			if err != nil || total != tc.total || capped != tc.capped {
				t.Fatalf("CountCapped = %d, %v, %v; se esperaba %d, %v", total, capped, err, tc.total, tc.capped)
			}
		})
	}
}

func TestCountCappedAcotaLaConsulta(t *testing.T) {
	q := &consultorDePrueba{}
	if _, _, err := CountCapped(context.Background(), q, "FROM t WHERE a = $1", []any{"x"}, 100); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.sql, "FROM t WHERE a = $1 LIMIT 101") || !strings.HasPrefix(q.sql, "SELECT count(*) FROM (SELECT 1 ") {
		t.Fatalf("la consulta no esta acotada por limit+1: %s", q.sql)
	}
	if len(q.args) != 1 || q.args[0] != "x" {
		t.Fatalf("argumentos: %v", q.args)
	}
}

func TestCountCappedPropagaElError(t *testing.T) {
	boom := errors.New("boom")
	q := &consultorDePrueba{fila: filaDePrueba{err: boom}}
	if _, _, err := CountCapped(context.Background(), q, "FROM t", nil, 100); !errors.Is(err, boom) {
		t.Fatalf("error: %v", err)
	}
}
