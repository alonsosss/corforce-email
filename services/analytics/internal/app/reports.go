package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/analytics/internal/domain"
	"github.com/google/uuid"
)

// ParseRange lee el rango de una consulta con el reloj del servicio.
func (uc *UseCase) ParseRange(from, to string) (domain.Range, error) {
	return domain.ParseRange(from, to, uc.now())
}

// Overview devuelve los totales del rango.
func (uc *UseCase) Overview(ctx context.Context, tenantID uuid.UUID, q domain.ClassQuery) (domain.Counters, error) {
	return uc.reports.Totals(ctx, tenantID, q)
}

// Timeseries devuelve un punto por dia del rango.
func (uc *UseCase) Timeseries(ctx context.Context, tenantID uuid.UUID, q domain.ClassQuery) ([]domain.DayCounters, error) {
	return uc.reports.Series(ctx, tenantID, q)
}

// Campaigns devuelve una pagina de las campanas con datos y el total.
func (uc *UseCase) Campaigns(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]domain.CampaignSummary, int64, error) {
	total, err := uc.reports.CountCampaigns(ctx, tenantID)
	if err != nil {
		return nil, 0, err
	}
	// Una pagina fuera del total no se consulta: ademas evita que un page enorme
	// desborde el desplazamiento.
	if total == 0 || int64(page-1) >= (total+int64(perPage)-1)/int64(perPage) {
		return []domain.CampaignSummary{}, total, nil
	}
	list, err := uc.reports.ListCampaigns(ctx, tenantID, perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// Campaign devuelve una campana con su serie diaria. Sin fechas, la serie cubre los dias
// con envios de la campana (domain.CampaignRange).
func (uc *UseCase) Campaign(ctx context.Context, tenantID, campaignID uuid.UUID, from, to string) (domain.CampaignDetail, error) {
	summary, err := uc.reports.GetCampaign(ctx, tenantID, campaignID)
	if err != nil {
		return domain.CampaignDetail{}, err
	}
	var r domain.Range
	if from == "" && to == "" {
		r = domain.CampaignRange(*summary, uc.now())
	} else if r, err = uc.ParseRange(from, to); err != nil {
		return domain.CampaignDetail{}, err
	}
	series, err := uc.reports.CampaignSeries(ctx, tenantID, campaignID, r)
	if err != nil {
		return domain.CampaignDetail{}, err
	}
	return domain.CampaignDetail{Summary: *summary, Range: r, Series: series}, nil
}

// Domains devuelve los dominios destino con mas envios del rango.
func (uc *UseCase) Domains(ctx context.Context, tenantID uuid.UUID, q domain.ClassQuery, limit int) ([]domain.DomainStats, error) {
	return uc.reports.TopDomains(ctx, tenantID, q, limit)
}
