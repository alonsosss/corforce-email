package keyring

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/crypto"
)

const (
	keyA = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	keyB = "1f1e1d1c1b1a191817161514131211100f0e0d0c0b0a09080706050403020100"
)

func ring(t *testing.T, active, old string) *crypto.MACKeyRing {
	t.Helper()
	t.Setenv("K_ACTIVE", active)
	t.Setenv("K_OLD", old)
	r, err := crypto.LoadMACKeyRing("K_ACTIVE", "K_OLD")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSinAnilloNoHayFirmante(t *testing.T) {
	if s := NewSigner(nil); s != nil {
		t.Fatal("sin llave el informe sale sin firma, no con un firmante vacio")
	}
}

// El informe se firma con HMAC-SHA256 de la llave ACTIVA, la misma que firma las filas: quien lo
// recibe lo comprueba con esa llave y el key_id le dice cual es.
func TestFirmaConLaLlaveActivaYSuIdentificador(t *testing.T) {
	kr := ring(t, keyA, keyB)
	s := NewSigner(kr)
	if s.KeyID() != kr.ActiveID() {
		t.Fatalf("key_id %s, esperado %s", s.KeyID(), kr.ActiveID())
	}
	data := []byte("format: 1\ngenerated_at: 2026-09-22T00:00:00Z\ncause: scheduled\n")
	raw, _ := hex.DecodeString(keyA)
	m := hmac.New(sha256.New, raw)
	m.Write(data)
	if got := s.Sign(data); !bytes.Equal(got, m.Sum(nil)) || len(got) != 32 {
		t.Fatalf("firma %x", got)
	}
	// La retirada no firma: un informe nuevo con una llave vieja no tendria sentido.
	rawB, _ := hex.DecodeString(keyB)
	mB := hmac.New(sha256.New, rawB)
	mB.Write(data)
	if bytes.Equal(s.Sign(data), mB.Sum(nil)) {
		t.Fatal("firmo con la llave retirada")
	}
	// Y lo firmado se vuelve a calcular por su id con el anillo, que es lo que hace el verificador.
	again, err := kr.SignWith(s.KeyID(), data)
	if err != nil || !bytes.Equal(again, s.Sign(data)) {
		t.Fatalf("%x %v", again, err)
	}
}

func TestDosBloquesDistintosDanFirmasDistintas(t *testing.T) {
	s := NewSigner(ring(t, keyA, ""))
	a := s.Sign([]byte("anchor: seq=10\n"))
	b := s.Sign([]byte("anchor: seq=11\n"))
	if bytes.Equal(a, b) {
		t.Fatal("la firma no distingue el contenido")
	}
	if !bytes.Equal(a, s.Sign([]byte("anchor: seq=10\n"))) {
		t.Fatal("la firma no es determinista")
	}
}
