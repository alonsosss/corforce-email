package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const remoteImageMailbox = "22222222-2222-4222-8222-222222222222"

func TestValidateRemoteImageURL(t *testing.T) {
	for raw, want := range map[string]string{
		"http://x.test/a.png":            "http://x.test/a.png",
		"HTTPS://x.test:443/a.png#frag":  "https://x.test:443/a.png",
		"http://x.test:80/a.png?b=1&c=2": "http://x.test:80/a.png?b=1&c=2",
	} {
		u, err := ValidateRemoteImageURL(raw)
		if err != nil || u.String() != want {
			t.Errorf("%s: %v %v", raw, u, err)
		}
	}
	for _, raw := range []string{
		"", "ftp://x.test/a.png", "http://x.test:8080/a.png", "https://x.test:80/a.png", "http://user:pw@x.test/a.png",
		"http:///a.png", "http://x.test/a\r\n.png", "javascript:alert(1)", "data:image/png;base64,AA", "cid:a@b",
		"https://x.test/" + strings.Repeat("a", MaxRemoteImageURLBytes),
	} {
		if _, err := ValidateRemoteImageURL(raw); !errors.Is(err, ErrRemoteImageRefused) {
			t.Errorf("%q: %v", raw, err)
		}
	}
}

func TestEnlaceFirmadoIdaYVuelta(t *testing.T) {
	key := DeriveRemoteImageKey([]byte("secreto-de-pruebas-de-32-caracteres"))
	now := time.Date(2026, 9, 24, 10, 7, 31, 0, time.UTC)
	link, err := NewRemoteImageLink("https://x.test/a.png#f", strings.ToUpper(remoteImageMailbox), now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if link.URL != "https://x.test/a.png" || link.MailboxID != remoteImageMailbox {
		t.Fatalf("normalizado: %+v", link)
	}
	if !link.Expires.Equal(time.Date(2026, 9, 24, 11, 15, 0, 0, time.UTC)) {
		t.Fatalf("caducidad redondeada al cuarto de hora siguiente: %v", link.Expires)
	}
	again, _ := NewRemoteImageLink("https://x.test/a.png", remoteImageMailbox, now.Add(5*time.Minute), time.Hour)
	if again.Sign(key) != link.Sign(key) {
		t.Fatal("el mismo mensaje abierto poco despues da la misma URL")
	}
	signed := link.Sign(key)
	if strings.ContainsAny(signed.EncodedURL+signed.Signature, "+/=") {
		t.Fatalf("base64url sin relleno: %+v", signed)
	}
	got, err := signed.Verify(key, now)
	if err != nil || got != link {
		t.Fatalf("%+v %v", got, err)
	}
	tampered := signed
	tampered.Signature = strings.Repeat("A", len(signed.Signature))
	short := signed
	short.Signature = signed.Signature[:10]
	badExpiry := signed
	badExpiry.Expires = "0" + signed.Expires
	for name, s := range map[string]SignedRemoteImage{"firma": tampered, "firma corta": short, "caducidad con cero": badExpiry} {
		if _, err := s.Verify(key, now); !errors.Is(err, ErrRemoteImageLinkInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := signed.Verify(key, link.Expires); !errors.Is(err, ErrRemoteImageLinkExpired) {
		t.Fatalf("vence en su instante: %v", err)
	}
}

func TestEnlaceNoSeFirmaSinBuzonNiPlazo(t *testing.T) {
	now := time.Now()
	if _, err := NewRemoteImageLink("https://x.test/a.png", "ana@x.test", now, time.Hour); !errors.Is(err, ErrRemoteImageLinkInvalid) {
		t.Fatalf("buzon: %v", err)
	}
	if _, err := NewRemoteImageLink("https://x.test/a.png", remoteImageMailbox, now, 0); !errors.Is(err, ErrRemoteImageLinkInvalid) {
		t.Fatalf("plazo: %v", err)
	}
	if _, err := NewRemoteImageLink("http://10.0.0.1:8080/", remoteImageMailbox, now, time.Hour); !errors.Is(err, ErrRemoteImageRefused) {
		t.Fatalf("url: %v", err)
	}
}

func TestSniffRemoteImage(t *testing.T) {
	for want, data := range map[string]string{
		"image/png":  "\x89PNG\r\n\x1a\nx",
		"image/jpeg": "\xFF\xD8\xFFx",
		"image/gif":  "GIF89ax",
		"image/webp": "RIFF\x00\x00\x00\x00WEBPx",
	} {
		if got, ok := SniffRemoteImage([]byte(data)); !ok || got != want {
			t.Errorf("%s: %s %v", want, got, ok)
		}
	}
	for _, data := range []string{"", "<svg></svg>", "<?xml version=\"1.0\"?><svg/>", "<html>", "BM\x00\x00"} {
		if _, ok := SniffRemoteImage([]byte(data)); ok {
			t.Errorf("%q no es un mapa de bits admitido", data)
		}
	}
}
