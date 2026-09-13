package nats

import (
	"encoding/json"
	"testing"
	"time"
)

func TestHasReasons(t *testing.T) {
	cases := map[string]bool{
		`{"email":"a@example.com","reason":"manual","reasons":["manual"]}`: true,
		`{"email":"a@example.com","reason":"manual","reasons":[]}`:         true,
		`{"email":"a@example.com","reason":"manual"}`:                      false,
		`{"email":"a@example.com","reason":"manual","reasons":null}`:       false,
		`{"email":"a@example.com","reason":"manual","reasons":"manual"}`:   false,
	}
	for raw, want := range cases {
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			t.Fatal(err)
		}
		if got := hasReasons(data); got != want {
			t.Errorf("%s: %v", raw, got)
		}
	}
}

func TestThrottle(t *testing.T) {
	th := throttle{every: time.Minute}
	t0 := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	if ok, skipped := th.allow(t0); !ok || skipped != 0 {
		t.Fatal("el primer aviso pasa")
	}
	for i := 1; i <= 3; i++ {
		if ok, _ := th.allow(t0.Add(time.Duration(i) * time.Second)); ok {
			t.Fatal("dentro del intervalo se calla")
		}
	}
	if ok, skipped := th.allow(t0.Add(time.Minute)); !ok || skipped != 3 {
		t.Fatalf("al cumplirse el intervalo avisa con los omitidos: %v %d", ok, skipped)
	}
}
