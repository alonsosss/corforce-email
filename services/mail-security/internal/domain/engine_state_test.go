package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestNormalizeSMTPNetworkComoLaBuscaRspamd(t *testing.T) {
	ok := map[string]string{
		"198.51.100.7":        "198.51.100.7",
		" 10.0.0.0/8 ":        "10.0.0.0/8",
		"203.0.113.9/24":      "203.0.113.0/24",
		"198.51.100.7/32":     "198.51.100.7",
		"::ffff:198.51.100.7": "198.51.100.7",
		"2001:db8::1/64":      "2001:db8::/64",
		"2001:DB8::1":         "2001:db8::1",
	}
	for in, want := range ok {
		p, err := NormalizeSMTPNetwork(in)
		if err != nil || SMTPNetworkField(p) != want {
			t.Errorf("%q -> %q %v, quiero %q", in, SMTPNetworkField(p), err, want)
		}
	}
	// Mas ancho de lo que SMTP_ACCESS compara, o no es una red: se rechaza.
	for _, in := range []string{"", "no-es-ip", "10.0.0.0/7", "0.0.0.0/0", "2001:db8::/31", "fe80::1%eth0"} {
		if _, err := NormalizeSMTPNetwork(in); !errors.Is(err, ErrValidation) {
			t.Errorf("%q deberia rechazarse: %v", in, err)
		}
	}
}

func TestNormalizeSMTPNetworksQuitaDuplicadosYAcota(t *testing.T) {
	got, err := NormalizeSMTPNetworks([]string{"198.51.100.7", "198.51.100.7/32", "203.0.113.0/24"})
	if err != nil || len(got) != 2 {
		t.Fatalf("duplicados: %v %v", got, err)
	}
	if _, err := NormalizeSMTPNetworks(nil); !errors.Is(err, ErrValidation) {
		t.Fatalf("lista vacia: %v", err)
	}
	many := make([]string, MaxSMTPAccessNetworks+1)
	for i := range many {
		many[i] = fmt.Sprintf("10.0.%d.1", i)
	}
	if _, err := NormalizeSMTPNetworks(many); !errors.Is(err, ErrValidation) {
		t.Fatalf("mas del tope: %v", err)
	}
}

func TestNextStampSoloAvanzaYConElContenido(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 500, time.UTC)
	seed := now.Add(-time.Hour)
	first, changed := NextStamp(nil, DocumentRspamdSettings, "h1", now, seed)
	if !changed || !first.LastModified.Equal(seed.Truncate(time.Second)) {
		t.Fatalf("primer documento: %+v %v", first, changed)
	}
	same, changed := NextStamp(&first, DocumentRspamdSettings, "h1", now, time.Time{})
	if changed || same != first {
		t.Fatalf("mismo contenido no cambia la marca: %+v", same)
	}
	second, changed := NextStamp(&first, DocumentRspamdSettings, "h2", now, time.Time{})
	if !changed || !second.LastModified.Equal(now.Truncate(time.Second)) {
		t.Fatalf("contenido nuevo: %+v", second)
	}
	// Dos cambios en el mismo segundo dan marcas distintas.
	third, _ := NextStamp(&second, DocumentRspamdSettings, "h3", now, time.Time{})
	if !third.LastModified.After(second.LastModified) {
		t.Fatalf("la marca debe avanzar: %v -> %v", second.LastModified, third.LastModified)
	}
	// Un reloj atrasado no hace retroceder la marca.
	late, _ := NextStamp(&third, DocumentRspamdSettings, "h4", now.Add(-time.Minute), time.Time{})
	if !late.LastModified.After(third.LastModified) {
		t.Fatalf("reloj atrasado: %v -> %v", third.LastModified, late.LastModified)
	}
}

// NaN o Inf no se pueden serializar: se descartan donde entran.
func TestParseFirewallBansDescartaVencimientosNoFinitos(t *testing.T) {
	bans := ParseFirewallBans(
		map[string]string{"198.51.100.0/24": "1893456000.5", "192.0.2.0/24": "NaN", "203.0.113.0/24": "+Inf"},
		map[string]string{"10.9.0.0/16": "1700000000"},
	)
	if len(bans) != 4 {
		t.Fatalf("baneos: %+v", bans)
	}
	byNet := map[string]FirewallBan{}
	for _, b := range bans {
		byNet[b.Network] = b
	}
	if byNet["198.51.100.0/24"].ExpiresAt == nil || byNet["192.0.2.0/24"].ExpiresAt != nil || byNet["203.0.113.0/24"].ExpiresAt != nil {
		t.Fatalf("vencimientos: %+v", bans)
	}
	if !byNet["10.9.0.0/16"].Permanent {
		t.Fatalf("permanente: %+v", byNet["10.9.0.0/16"])
	}
	if _, err := json.Marshal(bans); err != nil {
		t.Fatalf("la lista debe serializarse: %v", err)
	}
}

func TestFirewallOptionsValida(t *testing.T) {
	if err := DefaultFirewallOptions().Validate(); err != nil {
		t.Fatalf("los defectos de netfilter son validos: %v", err)
	}
	bad := DefaultFirewallOptions()
	bad.MaxBanTime = bad.BanTime - 1
	if err := bad.Validate(); !errors.Is(err, ErrValidation) {
		t.Fatalf("max_ban_time menor que ban_time: %v", err)
	}
	bad = DefaultFirewallOptions()
	bad.NetbanIPv4 = 4
	if err := bad.Validate(); !errors.Is(err, ErrValidation) {
		t.Fatalf("netban_ipv4 fuera de rango: %v", err)
	}
}
