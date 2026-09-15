package organizationcli

import (
	"context"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/alonsosss/corforce-email/pkg/tenantcell/tenantcelltest"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Solo la negativa definitiva de organization es una baja: una empresa de otra celda existe, y
// sin respuesta no se sabe.
func TestSoloLaNegativaDeOrganizationEsUnaBaja(t *testing.T) {
	enCelda, deOtra, nadie, sinRespuesta := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	org, url := tenantcelltest.New(t, "token-interno", map[string]string{enCelda.String(): "pe-01", deOtra.String(): "pe-02"})
	c := New(tenantcell.NewResolver(url, "token-interno", zap.NewNop()))
	ctx := context.Background()

	for nombre, id := range map[string]uuid.UUID{"de la celda": enCelda, "de otra celda": deOtra} {
		if gone, err := c.TenantGone(ctx, id); err != nil || gone {
			t.Errorf("empresa %s: retirada=%v %v", nombre, gone, err)
		}
	}
	if gone, err := c.TenantGone(ctx, nadie); err != nil || !gone {
		t.Errorf("empresa que organization no conoce: retirada=%v %v", gone, err)
	}
	org.SetDown(true)
	if gone, err := c.TenantGone(ctx, sinRespuesta); err == nil || gone {
		t.Errorf("sin respuesta: retirada=%v %v", gone, err)
	}
}
