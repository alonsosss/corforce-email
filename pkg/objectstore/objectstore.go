// Package objectstore es el cliente unico de almacenamiento de objetos de la plataforma
// sobre el protocolo S3 (MinIO en desarrollo, Amazon S3 en produccion). Centraliza
// la autenticacion (claves estaticas o cadena de credenciales IAM), el aislamiento
// multi-tenant por prefijo de clave y el acceso de lectura mediante URLs prefirmadas.
//
// Los buckets son privados (sin politica de lectura anonima). Los objetos public/ los
// sirve el gateway leyendolos del almacen (Stat y OpenLimited): la URL publica estable
// que se persiste apunta al gateway, no al bucket, y nunca expira ni expone el almacen.
// Los private/ se entregan con URL prefirmada de corta vida (ResolveDownloadURL).
package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MediaPathPrefix es el segmento de ruta del gateway que sirve los objetos.
// Las URLs estables persistidas tienen la forma {PublicBase}/{MediaPathPrefix}/{key}.
const MediaPathPrefix = "media"

// Config describe la conexion al almacen de objetos.
type Config struct {
	Endpoint   string
	AccessKey  string
	SecretKey  string
	Bucket     string
	Region     string
	PublicBase string
	UseSSL     bool
}

// Store es un cliente de objetos ligado a un unico bucket.
type Store struct {
	client     *minio.Client
	bucket     string
	region     string
	publicBase string
}

// New crea el cliente. Si AccessKey/SecretKey vienen vacios usa la cadena de
// credenciales del entorno (variables AWS_*, instance profile de EC2/ECS o web
// identity de EKS/IRSA), de modo que en AWS no haga falta poner secretos estaticos.
// La region es obligatoria para Amazon S3 (firma v4).
func New(cfg Config) (*Store, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("objectstore: endpoint requerido")
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("objectstore: bucket requerido")
	}

	var creds *credentials.Credentials
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		creds = credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, "")
	} else {
		creds = credentials.NewChainCredentials([]credentials.Provider{
			&credentials.EnvAWS{},
			&credentials.IAM{Client: &http.Client{Timeout: 10 * time.Second}},
		})
	}

	opts := &minio.Options{Creds: creds, Secure: cfg.UseSSL}
	if cfg.Region != "" {
		opts.Region = cfg.Region
	}

	client, err := minio.New(cfg.Endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("objectstore: cliente: %w", err)
	}

	return &Store{
		client:     client,
		bucket:     cfg.Bucket,
		region:     cfg.Region,
		publicBase: strings.TrimRight(cfg.PublicBase, "/"),
	}, nil
}

// FromEnv construye un Store a partir de las variables MINIO_*. Devuelve (nil, nil)
// si MINIO_ENDPOINT no esta definido, para que el almacen de objetos sea opcional
// y el servicio degrade con elegancia (subida deshabilitada) en lugar de fallar.
func FromEnv() (*Store, error) {
	endpoint := os.Getenv("MINIO_ENDPOINT")
	if endpoint == "" {
		return nil, nil
	}
	bucket := os.Getenv("MINIO_BUCKET")
	if bucket == "" {
		return nil, fmt.Errorf("objectstore: MINIO_BUCKET requerido cuando MINIO_ENDPOINT esta definido")
	}
	return New(Config{
		Endpoint:   endpoint,
		AccessKey:  os.Getenv("MINIO_ACCESS_KEY"),
		SecretKey:  os.Getenv("MINIO_SECRET_KEY"),
		Bucket:     bucket,
		Region:     os.Getenv("MINIO_REGION"),
		PublicBase: resolvePublicBase(),
		UseSSL:     os.Getenv("MINIO_USE_SSL") == "true",
	})
}

// resolvePublicBase obtiene la base publica estable (la del gateway). Descarta
// hosts internos/locales para no persistir URLs no resolubles por el cliente y cae
// al origen publico de la API (API_ORIGIN), que es donde vive el gateway.
func resolvePublicBase() string {
	fallback := strings.TrimRight(strings.TrimSpace(os.Getenv("API_ORIGIN")), "/")
	raw := strings.TrimRight(strings.TrimSpace(os.Getenv("MINIO_PUBLIC_URL")), "/")
	if raw == "" {
		return fallback
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return fallback
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "localhost", "127.0.0.1", "0.0.0.0", "minio":
		return fallback
	}
	return raw
}

// Bucket devuelve el nombre del bucket asociado.
func (s *Store) Bucket() string { return s.bucket }

// Verify comprueba que el bucket exista y sea accesible. No lo crea ni le aplica
// politicas: en produccion el bucket se aprovisiona fuera de la aplicacion (IaC)
// y es privado por diseno.
func (s *Store) Verify(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("objectstore: comprobar bucket: %w", err)
	}
	if !exists {
		return fmt.Errorf("objectstore: bucket %q no existe", s.bucket)
	}
	return nil
}

// Put almacena un objeto bajo la clave indicada.
func (s *Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	if _, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType}); err != nil {
		return fmt.Errorf("objectstore: put %q: %w", key, err)
	}
	return nil
}

// PutObject almacena un objeto y devuelve su URL publica estable (servida por el
// gateway). Variante de conveniencia para los servicios cuyo puerto de salida
// devuelve la URL directamente.
func (s *Store) PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) (string, error) {
	if err := s.Put(ctx, key, r, size, contentType); err != nil {
		return "", err
	}
	return s.PublicURL(key), nil
}

// Get abre un objeto para lectura, devolviendo su contenido, content-type y tamano.
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, string, int64, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, "", 0, fmt.Errorf("objectstore: get %q: %w", key, err)
	}
	stat, err := obj.Stat()
	if err != nil {
		obj.Close()
		return nil, "", 0, fmt.Errorf("objectstore: stat %q: %w", key, err)
	}
	return obj, stat.ContentType, stat.Size, nil
}

// ErrNotFound: la clave no existe en el bucket.
var ErrNotFound = errors.New("objectstore: objeto no encontrado")

// ErrTooLarge: el objeto supera el tope que el llamador admite leer.
var ErrTooLarge = errors.New("objectstore: objeto por encima del tope")

// ObjectInfo son los metadatos de un objeto. ETag va sin comillas, como lo da S3.
type ObjectInfo struct {
	Key          string
	ContentType  string
	Size         int64
	ETag         string
	LastModified time.Time
}

// Stat devuelve los metadatos de un objeto sin leer su contenido.
func (s *Store) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return ObjectInfo{}, clasificar("stat", key, err)
	}
	return objectInfo(info), nil
}

// OpenLimited abre un objeto para servirlo en flujo, negandose antes de leer un solo byte si
// declara mas de maxBytes. El lector entrega como mucho el tamano declarado: un objeto que
// cambiara entre la cabecera y el cuerpo no puede hacer leer de mas.
func (s *Store) OpenLimited(ctx context.Context, key string, maxBytes int64) (io.ReadCloser, ObjectInfo, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, ObjectInfo{}, clasificar("get", key, err)
	}
	stat, err := obj.Stat()
	if err != nil {
		obj.Close()
		return nil, ObjectInfo{}, clasificar("get", key, err)
	}
	info := objectInfo(stat)
	if info.Size < 0 || info.Size > maxBytes {
		obj.Close()
		return nil, info, fmt.Errorf("%w: %q declara %d bytes (tope %d)", ErrTooLarge, key, info.Size, maxBytes)
	}
	return limitedReadCloser{Reader: io.LimitReader(obj, info.Size), Closer: obj}, info, nil
}

type limitedReadCloser struct {
	io.Reader
	io.Closer
}

func objectInfo(info minio.ObjectInfo) ObjectInfo {
	return ObjectInfo{
		Key:          info.Key,
		ContentType:  info.ContentType,
		Size:         info.Size,
		ETag:         strings.Trim(info.ETag, `"`),
		LastModified: info.LastModified,
	}
}

func clasificar(op, key string, err error) error {
	switch minio.ToErrorResponse(err).Code {
	case "NoSuchKey", "NoSuchBucket", "NotFound":
		return fmt.Errorf("%w: %q", ErrNotFound, key)
	}
	return fmt.Errorf("objectstore: %s %q: %w", op, key, err)
}

// Delete elimina un objeto. No es error que la clave no exista.
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("objectstore: delete %q: %w", key, err)
	}
	return nil
}

// RemoveObject es un alias de Delete para los puertos que usan ese nombre.
func (s *Store) RemoveObject(ctx context.Context, key string) error { return s.Delete(ctx, key) }

// PresignGet genera una URL prefirmada de lectura temporal directa al bucket.
// Usada por el gateway para redirigir al cliente y por los flujos que entregan la
// URL a terceros (p. ej. la API de Meta para WhatsApp), donde el consumidor no
// puede autenticarse contra el gateway.
func (s *Store) PresignGet(ctx context.Context, key string, ttl time.Duration) (*url.URL, error) {
	u, err := s.client.PresignedGetObject(ctx, s.bucket, key, ttl, url.Values{})
	if err != nil {
		return nil, fmt.Errorf("objectstore: presign %q: %w", key, err)
	}
	return u, nil
}

// PublicURL construye la URL estable de un objeto, servida por el gateway.
func (s *Store) PublicURL(key string) string {
	return fmt.Sprintf("%s/%s/%s", s.publicBase, MediaPathPrefix, strings.TrimLeft(key, "/"))
}

// ExtractKey obtiene la clave de objeto a partir de una referencia almacenada:
// una clave gestionada (public/... o private/...) o una URL del gateway
// (.../media/<key>). Devuelve "" si la referencia no es un objeto gestionado
// (p. ej. una URL externa http(s)).
func (s *Store) ExtractKey(stored string) string {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return ""
	}
	if isManagedKey(stored) {
		return stored
	}
	marker := "/" + MediaPathPrefix + "/"
	if i := strings.Index(stored, marker); i >= 0 {
		key := stored[i+len(marker):]
		if isManagedKey(key) {
			return key
		}
	}
	return ""
}

func isManagedKey(s string) bool {
	return strings.HasPrefix(s, "public/") || strings.HasPrefix(s, "private/")
}

// ResolveDownloadURL convierte una referencia almacenada en una URL de descarga
// temporal. Si la referencia apunta a un objeto gestionado (clave o URL del
// gateway) devuelve una URL prefirmada de corta vida directa al bucket privado;
// cualquier otra cosa (URL externa) se devuelve sin cambios. El aislamiento
// multi-tenant lo garantiza el llamador: cada servicio solo resuelve referencias
// que su consulta —acotada por tenant— le devolvio.
func (s *Store) ResolveDownloadURL(ctx context.Context, stored string, ttl time.Duration) (string, error) {
	key := s.ExtractKey(stored)
	if key == "" {
		return stored, nil
	}
	u, err := s.PresignGet(ctx, key, ttl)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}
