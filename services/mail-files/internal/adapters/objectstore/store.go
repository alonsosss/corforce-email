// Package objectstore guarda el contenido de los ficheros compartidos en el almacen de objetos de la
// plataforma (pkg/objectstore), en el espacio private/ que el gateway nunca sirve.
package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/alonsosss/corforce-email/pkg/objectstore"
	"github.com/alonsosss/corforce-email/services/mail-files/internal/domain"
)

// contentType es el tipo con el que se guarda todo fichero: nunca el que declara el navegador, que
// podria convertir el objeto en algo que un cliente interprete.
const contentType = "application/octet-stream"

// objects es lo que se usa de pkg/objectstore.Store.
type objects interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	OpenLimited(ctx context.Context, key string, maxBytes int64) (io.ReadCloser, objectstore.ObjectInfo, error)
	Delete(ctx context.Context, key string) error
}

// Store implementa ports.ObjectStore.
type Store struct {
	objects objects
}

func New(o objects) *Store { return &Store{objects: o} }

func (s *Store) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	return s.objects.Put(ctx, key, r, size, contentType)
}

// Open abre el objeto negandose a leer mas que size, el tamano que se analizo y se guardo: un objeto
// cambiado por fuera no sale.
func (s *Store) Open(ctx context.Context, key string, size int64) (io.ReadCloser, error) {
	rc, info, err := s.objects.OpenLimited(ctx, key, size)
	switch {
	case errors.Is(err, objectstore.ErrNotFound):
		return nil, domain.ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("%w: almacen: %v", domain.ErrUnavailable, err)
	case info.Size != size:
		_ = rc.Close()
		return nil, fmt.Errorf("%w: el objeto %q mide %d bytes y se guardaron %d", domain.ErrUnavailable, key, info.Size, size)
	}
	return rc, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	return s.objects.Delete(ctx, key)
}
