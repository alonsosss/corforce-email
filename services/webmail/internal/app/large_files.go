package app

import (
	"context"
	"io"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// Los ficheros grandes por enlace viven en mail-files (docs/adr/0014), que los analiza con ClamAV,
// los guarda y sirve el enlace publico. El webmail los pide con la empresa y el buzon de la sesion,
// nunca con datos de la peticion, y entrega sus rechazos tal cual.

// LargeFiles devuelve los enlaces del buzon, su uso y los topes. Sin mail-files configurado la
// funcion aparece apagada y la interfaz no la ofrece.
func (s *Service) LargeFiles(ctx context.Context, sess domain.Session) (domain.LargeFileListing, error) {
	mb, err := mailboxOf(sess)
	if err != nil {
		return domain.LargeFileListing{}, err
	}
	if s.largeFiles == nil {
		return domain.LargeFileListing{Items: []domain.LargeFile{}}, nil
	}
	listing, err := s.largeFiles.ListLargeFiles(ctx, mb)
	if err != nil {
		return domain.LargeFileListing{}, s.davError("no se pudieron leer los ficheros compartidos", sess, err)
	}
	return listing, nil
}

// UploadLargeFile lleva el fichero en flujo a mail-files y devuelve su enlace.
func (s *Service) UploadLargeFile(ctx context.Context, sess domain.Session, name string, body io.Reader, opts domain.LargeFileOptions) (domain.LargeFile, error) {
	mb, err := mailboxOf(sess)
	if err != nil {
		return domain.LargeFile{}, err
	}
	if s.largeFiles == nil {
		return domain.LargeFile{}, domain.ErrLargeFilesDisabled
	}
	file, err := s.largeFiles.UploadLargeFile(ctx, mb, name, body, opts)
	if err != nil {
		return domain.LargeFile{}, s.davError("no se pudo compartir un fichero grande", sess, err)
	}
	return file, nil
}

// RevokeLargeFile cierra un enlace del buzon: deja de descargarse en el acto.
func (s *Service) RevokeLargeFile(ctx context.Context, sess domain.Session, id string) (domain.LargeFile, error) {
	mb, err := mailboxOf(sess)
	if err != nil {
		return domain.LargeFile{}, err
	}
	if s.largeFiles == nil {
		return domain.LargeFile{}, domain.ErrLargeFilesDisabled
	}
	if !domain.ValidUUID(id) {
		return domain.LargeFile{}, &domain.ServiceRejection{Kind: domain.RejectNotFound, Code: "FILE_NOT_FOUND", Message: "el fichero no existe"}
	}
	file, err := s.largeFiles.RevokeLargeFile(ctx, mb, id)
	if err != nil {
		return domain.LargeFile{}, s.davError("no se pudo revocar un fichero compartido", sess, err)
	}
	return file, nil
}
