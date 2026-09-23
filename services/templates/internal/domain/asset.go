package domain

import (
	"bytes"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/image/webp"
)

// Limites de una imagen subida para las plantillas.
const (
	MaxAssetBytes      = 5 << 20
	MaxAssetDimension  = 4000
	MaxAssetNameLength = 255
	DefaultAssetPage   = 50
	MaxAssetPage       = 100
)

// Asset es una imagen de la empresa guardada en el almacen de objetos con la clave ObjectKey.
type Asset struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	SHA256      string
	ObjectKey   string
	ContentType string
	SizeBytes   int
	Width       int
	Height      int
	Name        string
	CreatedBy   uuid.UUID
	CreatedAt   time.Time
}

// ImageInfo es lo que se sabe de una imagen tras comprobar su firma y su cabecera.
type ImageInfo struct {
	ContentType string
	Ext         string
	Width       int
	Height      int
}

type imageFormat struct {
	contentType, ext string
	matches          func([]byte) bool
	decodeConfig     func(io.Reader) (image.Config, error)
}

// imageFormats: el tipo se decide por la firma de los primeros bytes, nunca por la extension ni
// por el Content-Type que declare el navegador.
var imageFormats = []imageFormat{
	{"image/png", "png", func(b []byte) bool { return bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) }, png.DecodeConfig},
	{"image/jpeg", "jpg", func(b []byte) bool { return bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}) }, jpeg.DecodeConfig},
	{"image/gif", "gif", func(b []byte) bool {
		return bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a"))
	}, gif.DecodeConfig},
	{"image/webp", "webp", func(b []byte) bool {
		return len(b) >= 12 && bytes.Equal(b[0:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP"))
	}, webp.DecodeConfig},
}

// AssetContentTypes son los tipos admitidos, en orden estable.
func AssetContentTypes() []string {
	out := make([]string, 0, len(imageFormats))
	for _, f := range imageFormats {
		out = append(out, f.contentType)
	}
	return out
}

// InspectImage comprueba que data sea una imagen admitida por su firma, lee sus dimensiones de
// la cabecera y aplica los topes de tamano y de dimensiones.
func InspectImage(data []byte) (ImageInfo, error) {
	if len(data) == 0 {
		return ImageInfo{}, fmt.Errorf("%w: el fichero esta vacio", ErrInvalidAsset)
	}
	if len(data) > MaxAssetBytes {
		return ImageInfo{}, fmt.Errorf("%w: la imagen supera %d bytes", ErrInvalidAsset, MaxAssetBytes)
	}
	for _, f := range imageFormats {
		if !f.matches(data) {
			continue
		}
		cfg, err := f.decodeConfig(bytes.NewReader(data))
		if err != nil {
			return ImageInfo{}, fmt.Errorf("%w: la cabecera de la imagen %s no es valida", ErrInvalidAsset, f.ext)
		}
		if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxAssetDimension || cfg.Height > MaxAssetDimension {
			return ImageInfo{}, fmt.Errorf("%w: las dimensiones %dx%d superan %dx%d", ErrInvalidAsset, cfg.Width, cfg.Height, MaxAssetDimension, MaxAssetDimension)
		}
		return ImageInfo{ContentType: f.contentType, Ext: f.ext, Width: cfg.Width, Height: cfg.Height}, nil
	}
	return ImageInfo{}, fmt.Errorf("%w: solo se admiten PNG, JPEG, GIF y WebP", ErrInvalidAsset)
}

// AssetObjectKey es la clave del objeto: por contenido y bajo public/, que es lo unico que el
// gateway sirve sin sesion (los correos se abren fuera de la aplicacion).
func AssetObjectKey(tenantID uuid.UUID, sha256Hex, ext string) string {
	return "public/" + tenantID.String() + "/templates/" + sha256Hex + "." + ext
}

// NormalizeAssetName deja del nombre del fichero solo la parte final, sin caracteres de control
// y acotada. Sin nombre utilizable, "imagen.<ext>".
func NormalizeAssetName(name, ext string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	name = strings.TrimSpace(path.Base(name))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == "/" {
		return "imagen." + ext
	}
	if utf8.RuneCountInString(name) > MaxAssetNameLength {
		name = string([]rune(name)[:MaxAssetNameLength])
	}
	return name
}
