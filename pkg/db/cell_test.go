package db

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/config"
	"go.uber.org/zap"
)

// El puerto 1 no escucha: si NewCellPool llegara a conectar, el error seria otro.
func TestNewCellPoolFallaCerradoSinCredencialDeCelda(t *testing.T) {
	pg := config.PostgresConfig{Host: "127.0.0.1", Port: 1, User: "mail_admin", Password: "platform-pass", CellDBName: "mail_cell_pe_01"}
	pool, err := NewCellPool(context.Background(), pg, zap.NewNop())
	if pool != nil {
		pool.Close()
		t.Fatal("no debe abrirse pool sin credencial de celda")
	}
	if !errors.Is(err, config.ErrCellCredentialRequired) {
		t.Fatalf("error %v, se esperaba %v", err, config.ErrCellCredentialRequired)
	}
}
