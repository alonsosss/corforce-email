package domain

import (
	"errors"
	"fmt"
	"testing"
)

func TestNormalizeAddress(t *testing.T) {
	ok := map[string]string{
		"Ana@Destino.TEST":      "Ana@destino.test",
		" envios@empresa.test ": "envios@empresa.test",
	}
	for in, want := range ok {
		if got, valid := NormalizeAddress(in); !valid || got != want {
			t.Errorf("%q: %q %v", in, got, valid)
		}
	}
	for _, bad := range []string{"", "@x.test", "a@", "a@x", "a@.x.test", "a@x.test.", "a b@x.test", "<a@x.test>", "a@x.test,b@y.test",
		string(make([]byte, 65)) + "@x.test"} {
		if _, valid := NormalizeAddress(bad); valid {
			t.Errorf("%q no deberia admitirse", bad)
		}
	}
}

func TestIdempotencyKey(t *testing.T) {
	env := Envelope{From: "a@x.test", Recipients: []string{"b@y.test"}}
	k := IdempotencyKey("t1", "id@x.test", env)
	if k == "" || len(k) != len("smtp-")+64 || k != IdempotencyKey("t1", "id@x.test", env) {
		t.Fatalf("clave estable: %q", k)
	}
	if k == IdempotencyKey("t2", "id@x.test", env) || k == IdempotencyKey("t1", "otro@x.test", env) ||
		k == IdempotencyKey("t1", "id@x.test", Envelope{From: "a@x.test", Recipients: []string{"c@y.test"}}) {
		t.Fatal("empresa, Message-ID y sobre distinguen la clave")
	}
	if IdempotencyKey("t1", " ", env) != "" {
		t.Fatal("sin Message-ID no se deduplica")
	}
}

func TestAsRejection(t *testing.T) {
	if AsRejection(fmt.Errorf("x: %w", ErrInfected)) != ErrInfected {
		t.Fatal("un rechazo envuelto se reconoce")
	}
	if r := AsRejection(errors.New("otra cosa")); r != ErrUpstream || r.Kind != Temporary {
		t.Fatal("lo desconocido es temporal")
	}
	if ErrInfected.Error() == "" || ErrAuthFailed.Kind != Permanent {
		t.Fatal("clases")
	}
}
