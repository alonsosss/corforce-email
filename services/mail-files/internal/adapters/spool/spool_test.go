package spool

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLaSubidaSeReleeYSeBorra(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("contenido")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		r, err := f.Rewind()
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := io.ReadAll(r); string(got) != "contenido" {
			t.Fatalf("lectura %d: %q", i, got)
		}
	}
	if err := f.Discard(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("quedan temporales: %v", entries)
	}
}

func TestDirectorioInexistenteONoEscribible(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "no-existe"), time.Hour); err == nil {
		t.Fatal("directorio inexistente aceptado")
	}
	if _, err := New("", time.Hour); err == nil {
		t.Fatal("directorio vacio aceptado")
	}
	if os.Geteuid() == 0 {
		t.Skip("root escribe en un directorio de solo lectura")
	}
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	if _, err := New(ro, time.Hour); err == nil {
		t.Fatal("directorio de solo lectura aceptado")
	}
}

func TestAlArrancarSeBorranLasSubidasAbandonadas(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "upload-viejo")
	recent := filepath.Join(dir, "upload-reciente")
	other := filepath.Join(dir, "otro-fichero")
	for _, p := range []string{old, recent, other} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, past, past); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("la subida abandonada sigue")
	}
	for _, p := range []string{recent, other} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("se borro %s: %v", p, err)
		}
	}
}
