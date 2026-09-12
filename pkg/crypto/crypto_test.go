package crypto

import (
	"bytes"
	"errors"
	"testing"
)

const (
	llaveA = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	llaveB = "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f"
	llaveC = "404142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f"
)

func anillo(t *testing.T, activa, viejas string) *KeyRing {
	t.Helper()
	t.Setenv("PRUEBA_KEY", activa)
	t.Setenv("PRUEBA_KEYS_OLD", viejas)
	kr, err := LoadKeyRing("PRUEBA_KEY", "PRUEBA_KEYS_OLD")
	if err != nil {
		t.Fatalf("LoadKeyRing: %v", err)
	}
	return kr
}

// Lo cifrado con la llave anterior se re-cifra bajo la activa y, a partir de ahi, se lee
// SIN la vieja: es lo que permite retirarla.
func TestRotateRecifraBajoLaLlaveActiva(t *testing.T) {
	anterior := anillo(t, llaveA, "")
	dato, err := anterior.Encrypt([]byte("contrasena-del-portal"))
	if err != nil {
		t.Fatal(err)
	}

	rotando := anillo(t, llaveB, llaveA)
	if !rotando.HasOldKeys() {
		t.Fatal("HasOldKeys deberia ser true con una llave vieja configurada")
	}
	nuevo, rotado, err := rotando.Rotate(dato)
	if err != nil || !rotado {
		t.Fatalf("Rotate: rotado=%v err=%v; want true, nil", rotado, err)
	}
	if bytes.Equal(nuevo, dato) {
		t.Fatal("el dato re-cifrado no puede ser identico al viejo")
	}

	// Segunda pasada: ya esta bajo la activa, no se toca.
	otra, rotado, err := rotando.Rotate(nuevo)
	if err != nil || rotado || !bytes.Equal(otra, nuevo) {
		t.Fatalf("segunda pasada: rotado=%v err=%v identico=%v; want false, nil, true", rotado, err, bytes.Equal(otra, nuevo))
	}

	retirada := anillo(t, llaveB, "")
	if retirada.HasOldKeys() {
		t.Fatal("HasOldKeys deberia ser false sin llaves viejas")
	}
	plano, err := retirada.Decrypt(nuevo)
	if err != nil || string(plano) != "contrasena-del-portal" {
		t.Fatalf("tras retirar la vieja: %q err=%v", plano, err)
	}
	if _, err := retirada.Decrypt(dato); !errors.Is(err, ErrUndecryptable) {
		t.Fatalf("el dato viejo sin la llave vieja deberia ser ilegible, got %v", err)
	}
}

// Un dato que no abre ninguna llave del anillo es un error explicito, no un "sin cambios":
// el que rota tiene que contarlo antes de retirar nada.
func TestRotateSinLlaveQueAbraEsError(t *testing.T) {
	ajeno := anillo(t, llaveC, "")
	dato, err := ajeno.Encrypt([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	kr := anillo(t, llaveB, llaveA)
	out, rotado, err := kr.Rotate(dato)
	if !errors.Is(err, ErrUndecryptable) || rotado || out != nil {
		t.Fatalf("got out=%v rotado=%v err=%v; want nil, false, ErrUndecryptable", out, rotado, err)
	}
}
