package sesclient

import (
	"context"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
)

// reputationWindow es cuanto hacia atras se busca el ultimo dato de reputacion: SES publica
// Reputation.* en CloudWatch con retraso y sin dato cuando no hubo envios.
const reputationWindow = 6 * time.Hour

type accountAPI interface {
	GetAccount(ctx context.Context, in *sesv2.GetAccountInput, opts ...func(*sesv2.Options)) (*sesv2.GetAccountOutput, error)
}

type metricsAPI interface {
	GetMetricData(ctx context.Context, in *cloudwatch.GetMetricDataInput, opts ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error)
}

// AccountReader implementa ports.SESAccountReader con GetAccount de SES y las metricas de
// reputacion de la cuenta en CloudWatch (espacio AWS/SES, sin dimensiones).
type AccountReader struct {
	ses accountAPI
	cw  metricsAPI
	now func() time.Time
}

func NewAccountReader(ctx context.Context, opt Options) (*AccountReader, error) {
	cfg, err := loadConfig(ctx, opt)
	if err != nil {
		return nil, err
	}
	return &AccountReader{ses: sesv2.NewFromConfig(cfg), cw: cloudwatch.NewFromConfig(cfg), now: time.Now}, nil
}

func (a *AccountReader) AccountStatus(ctx context.Context) (domain.SESAccountStatus, error) {
	acc, err := a.ses.GetAccount(ctx, &sesv2.GetAccountInput{})
	if err != nil {
		return domain.SESAccountStatus{}, fmt.Errorf("SES GetAccount: %w", err)
	}
	status := domain.SESAccountStatus{SendingEnabled: acc.SendingEnabled, ProductionAccess: acc.ProductionAccessEnabled}
	if q := acc.SendQuota; q != nil {
		status.Max24HourSend, status.SentLast24Hours, status.MaxSendRate = q.Max24HourSend, q.SentLast24Hours, q.MaxSendRate
	}
	end := a.now().UTC()
	out, err := a.cw.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(end.Add(-reputationWindow)),
		EndTime:   aws.Time(end),
		ScanBy:    cwtypes.ScanByTimestampDescending,
		MetricDataQueries: []cwtypes.MetricDataQuery{
			reputationQuery("bounce", "Reputation.BounceRate"),
			reputationQuery("complaint", "Reputation.ComplaintRate"),
		},
	})
	if err != nil {
		return domain.SESAccountStatus{}, fmt.Errorf("CloudWatch GetMetricData: %w", err)
	}
	for _, r := range out.MetricDataResults {
		if r.Id == nil || len(r.Values) == 0 {
			continue
		}
		latest := r.Values[0]
		switch *r.Id {
		case "bounce":
			status.BounceRate = &latest
		case "complaint":
			status.ComplaintRate = &latest
		}
	}
	return status, nil
}

func reputationQuery(id, metric string) cwtypes.MetricDataQuery {
	return cwtypes.MetricDataQuery{
		Id: aws.String(id),
		MetricStat: &cwtypes.MetricStat{
			Metric: &cwtypes.Metric{Namespace: aws.String("AWS/SES"), MetricName: aws.String(metric)},
			Period: aws.Int32(3600),
			Stat:   aws.String("Average"),
		},
		ReturnData: aws.Bool(true),
	}
}
