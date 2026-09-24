// Package app son los casos de uso de mail-files: subir un fichero grande (analizado por ClamAV
// antes de guardarlo), listar y revocar los enlaces del buzon, servir la descarga publica y barrer
// lo caducado.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-files/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-files/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Config son los topes y plazos del servicio.
type Config struct {
	Policy domain.Policy
	// MaxConcurrentUploads acota las subidas simultaneas: cada una ocupa hasta Policy.MaxFileBytes en
	// el directorio temporal.
	MaxConcurrentUploads int
	// ListLimit es cuantos enlaces, del mas reciente al mas antiguo, ve el remitente.
	ListLimit int
	// PendingGrace es lo que se espera a una subida pending antes de darla por fallida.
	PendingGrace time.Duration
	// DeleteGrace es lo que se espera tras caducar o agotarse un enlace antes de borrar el objeto, para
	// no cortar una descarga que empezo justo antes.
	DeleteGrace time.Duration
	// HistoryRetention es cuanto se conserva la fila de un enlace cerrado cuyo objeto ya se borro.
	HistoryRetention time.Duration
	// SweepInterval es cada cuanto corre el barrido; 0 lo desactiva.
	SweepInterval time.Duration
	// SweepBatch es cuantos objetos se borran por empresa y pasada.
	SweepBatch int
}

func (c Config) validate() error {
	if err := c.Policy.Validate(); err != nil {
		return err
	}
	if c.MaxConcurrentUploads < 1 || c.ListLimit < 1 || c.PendingGrace <= 0 || c.DeleteGrace < 0 ||
		c.HistoryRetention <= 0 || c.SweepInterval < 0 || c.SweepBatch < 1 {
		return errors.New("mail-files: configuracion de subidas o barrido incoherente")
	}
	return nil
}

type Deps struct {
	Repo    ports.Repository
	Binder  ports.TenantBinder
	Tenants ports.Tenants
	Leader  ports.LeaderLock
	// Store nil deja el servicio sin almacen: no se sube ni se descarga nada.
	Store   ports.ObjectStore
	Scanner ports.Scanner
	Spool   ports.Spool
	Links   *domain.LinkSigner
	Now     func() time.Time
	Config  Config
	Logger  *zap.Logger
}

type UseCase struct {
	repo    ports.Repository
	binder  ports.TenantBinder
	tenants ports.Tenants
	leader  ports.LeaderLock
	store   ports.ObjectStore
	scanner ports.Scanner
	spool   ports.Spool
	links   *domain.LinkSigner
	now     func() time.Time
	cfg     Config
	slots   chan struct{}
	logger  *zap.Logger
}

func New(d Deps) (*UseCase, error) {
	if d.Repo == nil || d.Binder == nil || d.Scanner == nil || d.Spool == nil || d.Links == nil || d.Logger == nil {
		return nil, errors.New("mail-files: faltan dependencias")
	}
	if err := d.Config.validate(); err != nil {
		return nil, err
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &UseCase{
		repo: d.Repo, binder: d.Binder, tenants: d.Tenants, leader: d.Leader, store: d.Store,
		scanner: d.Scanner, spool: d.Spool, links: d.Links, now: now, cfg: d.Config,
		slots: make(chan struct{}, d.Config.MaxConcurrentUploads), logger: d.Logger,
	}, nil
}

// Enabled dice si hay almacen: sin el, la interfaz no ofrece la funcion.
func (uc *UseCase) Enabled() bool { return uc.store != nil }

// Policy son los topes que se aplican, para servirlos.
func (uc *UseCase) Policy() domain.Policy { return uc.cfg.Policy }

func (uc *UseCase) shared(f domain.File) domain.SharedFile {
	return domain.SharedFile{File: f, URL: uc.links.URL(domain.LinkClaims{TenantID: f.TenantID, FileID: f.ID, ExpiresAt: f.ExpiresAt})}
}

// Upload guarda un fichero grande del buzon y devuelve su enlace. Orden: se recibe entero en el
// directorio temporal con su huella, ClamAV lo analiza, se reserva en la cuota (pending), se sube al
// almacen y queda listo. Sin veredicto limpio no se reserva ni se sube nada.
func (uc *UseCase) Upload(ctx context.Context, owner domain.Owner, rawName string, body io.Reader, opts domain.UploadOptions) (domain.SharedFile, error) {
	if uc.store == nil {
		return domain.SharedFile{}, domain.ErrStorageDisabled
	}
	name, err := domain.SafeFileName(rawName)
	if err != nil {
		return domain.SharedFile{}, err
	}
	expiresIn, maxDownloads, err := uc.cfg.Policy.Resolve(opts)
	if err != nil {
		return domain.SharedFile{}, err
	}
	select {
	case uc.slots <- struct{}{}:
		defer func() { <-uc.slots }()
	default:
		return domain.SharedFile{}, domain.ErrBusy
	}
	bound, err := uc.binder.Bind(ctx, owner.TenantID, owner.MailboxID)
	if err != nil {
		return domain.SharedFile{}, err
	}

	spooled, err := uc.spool.Create()
	if err != nil {
		return domain.SharedFile{}, fmt.Errorf("%w: directorio temporal: %v", domain.ErrUnavailable, err)
	}
	defer func() {
		if derr := spooled.Discard(); derr != nil {
			uc.logger.Warn("mail-files: no se pudo borrar una subida temporal", zap.Error(derr))
		}
	}()
	digest := sha256.New()
	size, err := io.Copy(io.MultiWriter(spooled, digest), io.LimitReader(body, uc.cfg.Policy.MaxFileBytes+1))
	if err != nil {
		return domain.SharedFile{}, fmt.Errorf("%w: %v", domain.ErrIncomplete, err)
	}
	switch {
	case size > uc.cfg.Policy.MaxFileBytes:
		return domain.SharedFile{}, domain.ErrTooLarge
	case size == 0:
		return domain.SharedFile{}, domain.ErrEmpty
	}

	content, err := spooled.Rewind()
	if err != nil {
		return domain.SharedFile{}, fmt.Errorf("%w: releer la subida: %v", domain.ErrUnavailable, err)
	}
	if err := uc.scanner.Scan(ctx, content); err != nil {
		if errors.Is(err, domain.ErrInfected) {
			uc.logger.Warn("mail-files: fichero infectado rechazado",
				zap.String("tenant_id", owner.TenantID.String()), zap.String("mailbox_id", owner.MailboxID.String()), zap.Error(err))
			return domain.SharedFile{}, domain.ErrInfected
		}
		return domain.SharedFile{}, fmt.Errorf("%w: %v", domain.ErrScanUnavailable, err)
	}

	now := uc.now()
	id := uuid.New()
	file := domain.File{
		ID: id, TenantID: owner.TenantID, MailboxID: owner.MailboxID, Name: name,
		SizeBytes: size, SHA256: hex.EncodeToString(digest.Sum(nil)), ObjectKey: domain.ObjectKey(owner.TenantID, id),
		Status: domain.StatusPending, ExpiresAt: now.Add(expiresIn).Truncate(time.Second), MaxDownloads: maxDownloads,
		CreatedAt: now,
	}
	if err := uc.repo.CreatePending(bound, file, uc.cfg.Policy, now); err != nil {
		return domain.SharedFile{}, err
	}
	if content, err = spooled.Rewind(); err == nil {
		err = uc.store.Put(ctx, file.ObjectKey, content, size)
	}
	if err != nil {
		// La fila queda fallida para liberar la cuota; si ni eso se puede, el barrido la cierra tras
		// PendingGrace y borra lo que hubiera llegado al almacen.
		if ferr := uc.repo.MarkFailed(context.WithoutCancel(bound), owner.TenantID, id); ferr != nil {
			uc.logger.Warn("mail-files: subida fallida sin cerrar; la cerrara el barrido", zap.String("file_id", id.String()), zap.Error(ferr))
		}
		return domain.SharedFile{}, fmt.Errorf("%w: almacen: %v", domain.ErrUnavailable, err)
	}
	ready, err := uc.repo.MarkReady(bound, owner.TenantID, id)
	if err != nil {
		return domain.SharedFile{}, err
	}
	uc.logger.Info("mail-files: fichero compartido",
		zap.String("tenant_id", owner.TenantID.String()), zap.String("mailbox_id", owner.MailboxID.String()),
		zap.String("file_id", id.String()), zap.Int64("size", size))
	return uc.shared(ready), nil
}

// List devuelve los enlaces del buzon, su uso y la politica.
func (uc *UseCase) List(ctx context.Context, owner domain.Owner) (domain.Listing, error) {
	bound, err := uc.binder.Bind(ctx, owner.TenantID, owner.MailboxID)
	if err != nil {
		return domain.Listing{}, err
	}
	files, err := uc.repo.ListByMailbox(bound, owner, uc.cfg.ListLimit)
	if err != nil {
		return domain.Listing{}, err
	}
	usage, err := uc.repo.Usage(bound, owner, uc.now())
	if err != nil {
		return domain.Listing{}, err
	}
	out := domain.Listing{Items: make([]domain.SharedFile, len(files)), Usage: usage, Policy: uc.cfg.Policy}
	for i, f := range files {
		out.Items[i] = uc.shared(f)
	}
	return out, nil
}

// Revoke cierra un enlace del buzon y borra el objeto en el acto; si el borrado falla, lo repite el
// barrido. Una descarga en curso se corta: es lo que pide quien revoca.
func (uc *UseCase) Revoke(ctx context.Context, owner domain.Owner, id uuid.UUID) (domain.SharedFile, error) {
	bound, err := uc.binder.Bind(ctx, owner.TenantID, owner.MailboxID)
	if err != nil {
		return domain.SharedFile{}, err
	}
	file, err := uc.repo.Revoke(bound, owner, id, uc.now())
	if err != nil {
		return domain.SharedFile{}, err
	}
	uc.deleteObject(bound, file)
	return uc.shared(file), nil
}

func (uc *UseCase) deleteObject(ctx context.Context, f domain.File) {
	if uc.store == nil {
		return
	}
	if err := uc.store.Delete(ctx, f.ObjectKey); err != nil {
		uc.logger.Warn("mail-files: objeto no borrado; lo repetira el barrido", zap.String("file_id", f.ID.String()), zap.Error(err))
		return
	}
	if err := uc.repo.MarkObjectDeleted(ctx, f.TenantID, f.ID); err != nil {
		uc.logger.Warn("mail-files: objeto borrado sin marcar; lo marcara el barrido", zap.String("file_id", f.ID.String()), zap.Error(err))
	}
}

// resolve verifica el enlace antes de tocar ninguna base: una firma que no cuadra no llega a
// resolver la empresa, y la caducidad firmada se juzga con el reloj del servicio.
func (uc *UseCase) resolve(ctx context.Context, c domain.LinkClaims, signature string) (context.Context, domain.File, error) {
	if uc.store == nil {
		return nil, domain.File{}, domain.ErrLinkInvalid
	}
	now := uc.now()
	if !uc.links.Verify(c, signature) || !now.Before(c.ExpiresAt) {
		return nil, domain.File{}, domain.ErrLinkInvalid
	}
	bound, err := uc.binder.Bind(ctx, c.TenantID, uuid.Nil)
	if err != nil {
		if errors.Is(err, domain.ErrTenantUnknown) {
			return nil, domain.File{}, domain.ErrLinkInvalid
		}
		return nil, domain.File{}, err
	}
	file, err := uc.repo.Get(bound, c.TenantID, c.FileID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, domain.File{}, domain.ErrLinkInvalid
	}
	if err != nil {
		return nil, domain.File{}, err
	}
	if !file.ExpiresAt.Equal(c.ExpiresAt) || !file.Downloadable(now) {
		return nil, domain.File{}, domain.ErrLinkInvalid
	}
	return bound, file, nil
}

// Inspect devuelve el fichero de un enlace vigente sin contar una descarga: es la pagina que abre el
// enlace (y que abren los analizadores de enlaces de los correos, que asi no gastan descargas).
func (uc *UseCase) Inspect(ctx context.Context, c domain.LinkClaims, signature string) (domain.File, error) {
	_, file, err := uc.resolve(ctx, c, signature)
	return file, err
}

// Download abre el contenido de un enlace vigente y cuenta la descarga. El objeto se abre antes de
// contar: si ya no esta, no se gasta una descarga. Quien llama cierra el lector.
func (uc *UseCase) Download(ctx context.Context, c domain.LinkClaims, signature string) (domain.File, io.ReadCloser, error) {
	bound, file, err := uc.resolve(ctx, c, signature)
	if err != nil {
		return domain.File{}, nil, err
	}
	content, err := uc.store.Open(ctx, file.ObjectKey, file.SizeBytes)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.File{}, nil, domain.ErrLinkInvalid
	}
	if err != nil {
		return domain.File{}, nil, err
	}
	claimed, err := uc.repo.ClaimDownload(bound, c.TenantID, c.FileID, uc.now())
	if err != nil {
		_ = content.Close()
		return domain.File{}, nil, err
	}
	return claimed, content, nil
}
