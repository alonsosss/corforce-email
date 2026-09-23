package sesclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sestypes "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

type fakeAccountAPI struct {
	out *sesv2.GetAccountOutput
	err error
}

func (f fakeAccountAPI) GetAccount(context.Context, *sesv2.GetAccountInput, ...func(*sesv2.Options)) (*sesv2.GetAccountOutput, error) {
	return f.out, f.err
}

type fakeMetricsAPI struct {
	in  *cloudwatch.GetMetricDataInput
	out *cloudwatch.GetMetricDataOutput
	err error
}

func (f *fakeMetricsAPI) GetMetricData(_ context.Context, in *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	f.in = in
	return f.out, f.err
}

func TestAccountStatusLeeCuotaYReputacion(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	cw := &fakeMetricsAPI{out: &cloudwatch.GetMetricDataOutput{MetricDataResults: []cwtypes.MetricDataResult{
		{Id: aws.String("bounce"), Values: []float64{0.021, 0.010}},
		{Id: aws.String("complaint"), Values: nil},
	}}}
	r := &AccountReader{
		ses: fakeAccountAPI{out: &sesv2.GetAccountOutput{
			SendingEnabled: true, ProductionAccessEnabled: false,
			SendQuota: &sestypes.SendQuota{Max24HourSend: 200, SentLast24Hours: 3, MaxSendRate: 1},
		}},
		cw:  cw,
		now: func() time.Time { return now },
	}
	s, err := r.AccountStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !s.SendingEnabled || s.ProductionAccess || s.Max24HourSend != 200 || s.SentLast24Hours != 3 || s.MaxSendRate != 1 {
		t.Fatalf("cuenta: %+v", s)
	}
	if s.BounceRate == nil || *s.BounceRate != 0.021 {
		t.Fatalf("se toma el dato mas reciente de rebotes: %v", s.BounceRate)
	}
	if s.ComplaintRate != nil {
		t.Fatalf("sin dato de quejas no es cero: %v", *s.ComplaintRate)
	}
	if cw.in.ScanBy != cwtypes.ScanByTimestampDescending || !cw.in.EndTime.Equal(now) || !cw.in.StartTime.Equal(now.Add(-reputationWindow)) {
		t.Fatalf("consulta a CloudWatch: %+v", cw.in)
	}
	for _, q := range cw.in.MetricDataQueries {
		if *q.MetricStat.Metric.Namespace != "AWS/SES" || len(q.MetricStat.Metric.Dimensions) != 0 {
			t.Fatalf("metrica de cuenta sin dimensiones: %+v", q.MetricStat.Metric)
		}
	}
}

func TestAccountStatusPropagaErrores(t *testing.T) {
	denied := errors.New("AccessDenied")
	r := &AccountReader{ses: fakeAccountAPI{err: denied}, cw: &fakeMetricsAPI{}, now: time.Now}
	if _, err := r.AccountStatus(context.Background()); !errors.Is(err, denied) {
		t.Fatalf("GetAccount: %v", err)
	}
	r = &AccountReader{ses: fakeAccountAPI{out: &sesv2.GetAccountOutput{}}, cw: &fakeMetricsAPI{err: denied}, now: time.Now}
	if _, err := r.AccountStatus(context.Background()); !errors.Is(err, denied) {
		t.Fatalf("GetMetricData: %v", err)
	}
}
