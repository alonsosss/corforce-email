package db

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/config"
	"go.uber.org/zap"
)

// cellPoolName etiqueta en las metricas el pool de la base de la celda.
const cellPoolName = "cell"

// NewCellPool abre el pool fijo de un servicio de CELDA contra su base (CELL_DB_NAME) con
// la credencial de la celda. Falla cerrado antes de tocar la red: fuera de desarrollo, un
// servicio de celda sin credencial propia no arranca (config.CellConnection).
func NewCellPool(ctx context.Context, pg config.PostgresConfig, logger *zap.Logger) (*Pool, error) {
	conn, err := pg.CellConnection()
	if err != nil {
		return nil, err
	}
	if conn.PlatformCredential {
		logger.Warn("servicio de celda con la credencial de plataforma: solo admisible en desarrollo; en produccion usa CELL_DB_PASSWORD",
			zap.String("cell_db", pg.CellDBName))
	}
	pool, err := NewNamedPool(ctx, conn.DSN, cellPoolName, logger)
	if err != nil {
		return nil, err
	}
	logger.Info("base de celda abierta", zap.String("cell_db", pg.CellDBName), zap.String("role", conn.User))
	return pool, nil
}
