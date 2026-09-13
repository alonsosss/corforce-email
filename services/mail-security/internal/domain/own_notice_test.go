package domain

import "testing"

func TestSplitOwnNotices(t *testing.T) {
	const notice = "cuarentena@acme.com"
	cases := []struct {
		sender string
		own    bool
	}{
		{"cuarentena@acme.com", true},
		{"CUARENTENA@Acme.com", true},
		{"<cuarentena@acme.com>", true},
		{"Aviso de cuarentena <cuarentena@acme.com>", true},
		{"  cuarentena@acme.com  ", true},
		{"otro@acme.com", false},
		{"cuarentena@acme.com.evil.example", false},
		{"", false},
		{"<>", false},
		{"no es una direccion", false},
	}
	for _, c := range cases {
		own, rest := SplitOwnNotices([]QuarantineItem{{Sender: c.sender}}, notice)
		if (len(own) == 1) != c.own || len(own)+len(rest) != 1 {
			t.Errorf("remitente %q: propio=%v, se esperaba %v", c.sender, len(own) == 1, c.own)
		}
	}
	// Sin remitente de aviso configurado nada es propio.
	if own, _ := SplitOwnNotices([]QuarantineItem{{Sender: ""}}, ""); len(own) != 0 {
		t.Fatal("un remitente vacio no puede coincidir con un aviso sin remitente")
	}
}
