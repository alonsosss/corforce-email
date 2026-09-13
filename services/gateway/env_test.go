package main

import "testing"

// Un limite o un umbral mal escrito impide arrancar: caer en silencio al valor por defecto
// dejaria el gateway con un cupo que nadie eligio.
func TestPositiveIntFromEnv(t *testing.T) {
	const key = "GATEWAY_TEST_POSITIVE_INT"
	casos := []struct {
		valor   string
		quiere  int
		esError bool
	}{
		{"", 30, false},
		{"50", 50, false},
		{"abc", 0, true},
		{"0", 0, true},
		{"-5", 0, true},
		{" 40", 0, true},
		{"4.5", 0, true},
	}
	for _, c := range casos {
		t.Setenv(key, c.valor)
		n, err := positiveIntFromEnv(key, 30)
		if (err != nil) != c.esError || (!c.esError && n != c.quiere) {
			t.Errorf("%q: n=%d err=%v; se esperaba n=%d error=%v", c.valor, n, err, c.quiere, c.esError)
		}
	}
}
