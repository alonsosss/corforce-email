package ports

import (
	"context"
	"io"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// LargeFiles son los ficheros grandes que el buzon comparte por enlace, en mail-files. Sus rechazos
// (topes, cuota, antivirus) llegan como *domain.ServiceRejection; un fallo de la llamada, como
// domain.ErrUnavailable.
type LargeFiles interface {
	ListLargeFiles(ctx context.Context, mb domain.MailboxRef) (domain.LargeFileListing, error)
	// UploadLargeFile lleva el fichero en flujo: no se carga entero en memoria.
	UploadLargeFile(ctx context.Context, mb domain.MailboxRef, name string, body io.Reader, opts domain.LargeFileOptions) (domain.LargeFile, error)
	RevokeLargeFile(ctx context.Context, mb domain.MailboxRef, id string) (domain.LargeFile, error)
}
