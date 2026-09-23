package domain

import (
	"errors"
	"math"
	"testing"
)

func TestSendResult(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, SendResultSent},
		{&SendError{Kind: ErrorTransient, Code: "TooManyRequestsException"}, SendResultThrottled},
		{&SendError{Kind: ErrorTransient, Code: "LimitExceededException"}, SendResultThrottled},
		{&SendError{Kind: ErrorTransient, Code: "SendingPausedException"}, SendResultPaused},
		{&SendError{Kind: ErrorTransient, Code: "AccountSuspendedException"}, SendResultPaused},
		{&SendError{Kind: ErrorTransient, Code: "Transport"}, SendResultTransient},
		{&SendError{Kind: ErrorPermanent, Code: "MessageRejected"}, SendResultPermanent},
		{errors.New("sin clasificar"), SendResultTransient},
	} {
		if got := SendResult(tc.err); got != tc.want {
			t.Errorf("%v: %s, se esperaba %s", tc.err, got, tc.want)
		}
	}
}

func TestSESAccountStatusValidate(t *testing.T) {
	ok := 0.02
	if err := (SESAccountStatus{Max24HourSend: 200, MaxSendRate: 1, BounceRate: &ok}).Validate(); err != nil {
		t.Fatalf("valido: %v", err)
	}
	if err := (SESAccountStatus{}).Validate(); err != nil {
		t.Fatalf("sin reputacion es valido: %v", err)
	}
	nan, inf, neg, big := math.NaN(), math.Inf(1), -0.1, 1.5
	for name, s := range map[string]SESAccountStatus{
		"NaN en la cuota":        {Max24HourSend: nan},
		"infinito en la tasa":    {MaxSendRate: inf},
		"negativo":               {SentLast24Hours: neg},
		"NaN en rebotes":         {BounceRate: &nan},
		"quejas por encima de 1": {ComplaintRate: &big},
	} {
		if err := s.Validate(); err == nil {
			t.Errorf("%s: deberia rechazarse", name)
		}
	}
}
