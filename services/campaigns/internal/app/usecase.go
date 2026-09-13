// Package app es el caso de uso de campaigns: el ciclo de vida de una campana, el
// orquestador que la entrega por lotes y las estadisticas que alimentan los eventos.
package app

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	// recordTimeout acota el registro del resultado de una llamada ya hecha. Corre sobre
	// un contexto que no se cancela con el apagado: un lote que transactional acepto debe
	// quedar escrito aunque el proceso este saliendo.
	recordTimeout = 15 * time.Second
	// dueBatch y runnablePerTick acotan cuanto trabajo toma un tick por empresa.
	dueBatch        = 100
	runnablePerTick = 100
)

type Config struct {
	// BatchSize es cuantos contactos se piden y entregan por lote (1..MaxBatchSize).
	BatchSize int
}

type Deps struct {
	Campaigns ports.CampaignRepository
	Batches   ports.BatchRepository
	Stats     ports.StatsRepository
	Tx        ports.Transactor
	Events    ports.EventPublisher
	Audience  ports.AudienceSource
	Sender    ports.BatchSender
	Templates ports.TemplateCatalog
	Config    Config
	Logger    *zap.Logger
	// Now permite fijar el reloj en las pruebas; nil = time.Now en UTC.
	Now func() time.Time
}

type UseCase struct {
	campaigns ports.CampaignRepository
	batches   ports.BatchRepository
	stats     ports.StatsRepository
	tx        ports.Transactor
	events    ports.EventPublisher
	audience  ports.AudienceSource
	sender    ports.BatchSender
	templates ports.TemplateCatalog
	batchSize int
	logger    *zap.Logger
	now       func() time.Time
}

func New(d Deps) *UseCase {
	now := d.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	size := d.Config.BatchSize
	if size < 1 || size > domain.MaxBatchSize {
		size = domain.MaxBatchSize
	}
	logger := d.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &UseCase{
		campaigns: d.Campaigns, batches: d.Batches, stats: d.Stats, tx: d.Tx, events: d.Events,
		audience: d.Audience, sender: d.Sender, templates: d.Templates,
		batchSize: size, logger: logger, now: now,
	}
}

// BatchSize es el tamano efectivo de lote (CAMPAIGNS_BATCH_SIZE acotado a MaxBatchSize).
func (uc *UseCase) BatchSize() int { return uc.batchSize }

func (uc *UseCase) Create(ctx context.Context, tenantID uuid.UUID, in domain.NewCampaignInput) (*domain.Campaign, error) {
	c, err := domain.NewCampaign(tenantID, in)
	if err != nil {
		return nil, err
	}
	if err := uc.campaigns.Insert(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (uc *UseCase) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Campaign, error) {
	return uc.campaigns.Get(ctx, tenantID, id)
}

func (uc *UseCase) List(ctx context.Context, tenantID uuid.UUID, f ports.ListFilter) ([]domain.Campaign, int64, error) {
	return uc.campaigns.List(ctx, tenantID, f)
}

func (uc *UseCase) Update(ctx context.Context, tenantID, id uuid.UUID, p domain.Patch) (*domain.Campaign, error) {
	var out *domain.Campaign
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		c, err := uc.campaigns.GetForUpdate(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if err := c.ApplyPatch(p); err != nil {
			return err
		}
		if err := uc.campaigns.Update(ctx, c); err != nil {
			return err
		}
		out = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (uc *UseCase) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		c, err := uc.campaigns.GetForUpdate(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if !c.Deletable() {
			return domain.ErrNotDeletable
		}
		return uc.campaigns.Delete(ctx, tenantID, id)
	})
}

func (uc *UseCase) ListBatches(ctx context.Context, tenantID, campaignID uuid.UUID, page, perPage int) ([]domain.Batch, int64, error) {
	if _, err := uc.campaigns.Get(ctx, tenantID, campaignID); err != nil {
		return nil, 0, err
	}
	return uc.batches.List(ctx, tenantID, campaignID, page, perPage)
}
