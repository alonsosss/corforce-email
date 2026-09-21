package domain

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestLaCredencialDeDestinoTieneFormaEstableYSoloElHashSeGuarda(t *testing.T) {
	c, err := NewDestinationCredential(bytes.NewReader(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	tenant, job := uuid.New(), uuid.New()
	token := c.Token(tenant, job)

	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != DestinationTokenPrefix || parts[1] != tenant.String() || parts[2] != job.String() || len(parts[3]) != 43 {
		t.Fatalf("forma: %q", token)
	}
	parsed, err := ParseDestinationToken(token)
	if err != nil || parsed.TenantID != tenant || parsed.JobID != job {
		t.Fatalf("parse: %+v %v", parsed, err)
	}
	if len(c.Hash) != 32 || !parsed.Matches(c.Hash) {
		t.Fatal("el hash del token no coincide con el guardado")
	}
	if bytes.Contains(c.Hash, bytes.Repeat([]byte{7}, 32)) {
		t.Fatal("el hash contiene el secreto")
	}
}

func TestDosCredencialesNuncaCoinciden(t *testing.T) {
	a, _ := NewDestinationCredential(bytes.NewReader(bytes.Repeat([]byte{1}, 32)))
	b, _ := NewDestinationCredential(bytes.NewReader(bytes.Repeat([]byte{2}, 32)))
	parsed, err := ParseDestinationToken(a.Token(uuid.New(), uuid.New()))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Matches(b.Hash) {
		t.Fatal("coincide con el hash de otro secreto")
	}
	if parsed.Matches(nil) || parsed.Matches(a.Hash[:16]) {
		t.Fatal("coincide con un hash ausente o truncado")
	}
}

func TestSinEntropiaNoSeEmiteCredencial(t *testing.T) {
	if _, err := NewDestinationCredential(io.LimitReader(bytes.NewReader(nil), 0)); err == nil {
		t.Fatal("emitio una credencial sin aleatoriedad")
	}
	if _, err := NewDestinationCredential(bytes.NewReader(make([]byte, 10))); err == nil {
		t.Fatal("emitio una credencial con aleatoriedad corta")
	}
}

func TestElTokenSoloTieneUnaEscrituraValida(t *testing.T) {
	c, _ := NewDestinationCredential(bytes.NewReader(bytes.Repeat([]byte{9}, 32)))
	tenant, job := uuid.New(), uuid.New()
	good := c.Token(tenant, job)
	secret := strings.Split(good, ".")[3]

	for name, token := range map[string]string{
		"vacio":                    "",
		"sin partes":               DestinationTokenPrefix,
		"prefijo distinto":         strings.Replace(good, DestinationTokenPrefix, "cfmj2", 1),
		"empresa en mayusculas":    strings.Join([]string{DestinationTokenPrefix, strings.ToUpper(tenant.String()), job.String(), secret}, "."),
		"trabajo sin guiones":      strings.Join([]string{DestinationTokenPrefix, tenant.String(), strings.ReplaceAll(job.String(), "-", ""), secret}, "."),
		"empresa nula":             strings.Join([]string{DestinationTokenPrefix, uuid.Nil.String(), job.String(), secret}, "."),
		"secreto corto":            strings.Join([]string{DestinationTokenPrefix, tenant.String(), job.String(), secret[:42]}, "."),
		"secreto con relleno":      strings.Join([]string{DestinationTokenPrefix, tenant.String(), job.String(), secret + "="}, "."),
		"secreto de otro alfabeto": strings.Join([]string{DestinationTokenPrefix, tenant.String(), job.String(), secret[:42] + "+"}, "."),
		"parte de mas":             good + ".x",
	} {
		if _, err := ParseDestinationToken(token); !errors.Is(err, ErrCredentialInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
