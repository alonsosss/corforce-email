// Package spool guarda cada subida en un fichero temporal, legible solo por el servicio, mientras ClamAV
// la analiza: nada llega al almacen de objetos sin veredicto limpio. Cada subida se borra al terminar,
// limpia o no, y las que dejo un proceso interrumpido se borran al arrancar.
package spool

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-files/internal/ports"
)

// Dir implementa ports.Spool sobre un directorio.
type Dir struct {
	path string
}

// uploadPrefix nombra los ficheros de subida; nada mas del directorio se toca.
const uploadPrefix = "upload-"

// New comprueba que el directorio exista y admita escribir (sin el ninguna subida funcionaria, y es
// mejor saberlo al arrancar) y borra las subidas de mas de staleAfter: las que dejo un proceso que se
// corto a mitad. Una subida en curso de otra replica nunca es tan antigua.
func New(path string, staleAfter time.Duration) (*Dir, error) {
	if path == "" {
		return nil, errors.New("directorio temporal de subidas sin definir")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	probe, err := os.CreateTemp(abs, "probe-*")
	if err != nil {
		return nil, fmt.Errorf("el directorio temporal de subidas %q no admite escribir: %w", abs, err)
	}
	name := probe.Name()
	_ = probe.Close()
	if err := os.Remove(name); err != nil {
		return nil, err
	}
	if err := purgeStale(abs, time.Now().Add(-staleAfter)); err != nil {
		return nil, err
	}
	return &Dir{path: abs}, nil
}

func purgeStale(dir string, before time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), uploadPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(before) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("borrar la subida abandonada %s: %w", e.Name(), err)
		}
	}
	return nil
}

func (d *Dir) Create() (ports.SpoolFile, error) {
	f, err := os.CreateTemp(d.path, uploadPrefix+"*")
	if err != nil {
		return nil, err
	}
	return &file{f: f}, nil
}

type file struct {
	f *os.File
}

func (s *file) Write(p []byte) (int, error) { return s.f.Write(p) }

func (s *file) Rewind() (io.Reader, error) {
	if _, err := s.f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return s.f, nil
}

func (s *file) Discard() error {
	closeErr := s.f.Close()
	if err := os.Remove(s.f.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return closeErr
}
