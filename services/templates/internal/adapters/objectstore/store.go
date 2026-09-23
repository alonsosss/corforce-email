// Package objectstore guarda las imagenes de las plantillas en el almacen de objetos de la
// plataforma (pkg/objectstore) y construye la URL con la que las cargan los correos.
package objectstore

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/objectstore"
)

// Store implementa ports.AssetStore sobre el bucket que sirve el gateway en /media/.
type Store struct {
	objects    *objectstore.Store
	publicBase string
}

// New exige una base publica absoluta: la URL de una imagen queda escrita en correos que se
// abren fuera de la aplicacion, meses despues, y tiene que resolver desde cualquier sitio.
func New(objects *objectstore.Store, publicBase string) (*Store, error) {
	base := strings.TrimRight(strings.TrimSpace(publicBase), "/")
	u, err := url.Parse(base)
	if base == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("PUBLIC_BASE_URL debe ser una URL absoluta http(s) sin consulta ni fragmento")
	}
	return &Store{objects: objects, publicBase: base}, nil
}

func (s *Store) Put(ctx context.Context, key string, data []byte, contentType string) error {
	return s.objects.Put(ctx, key, bytes.NewReader(data), int64(len(data)), contentType)
}

func (s *Store) PublicURL(key string) string {
	return s.publicBase + "/" + objectstore.MediaPathPrefix + "/" + strings.TrimLeft(key, "/")
}
