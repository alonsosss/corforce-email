package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/alonsosss/corforce-email/services/webmail/internal/ports"
	"go.uber.org/zap"
)

// CreateFolder crea una carpeta propia y la suscribe. Una subcarpeta ("Clientes/2026") crea los
// niveles intermedios que falten, como hace el servidor.
func (s *Service) CreateFolder(ctx context.Context, sess domain.Session, name string) (domain.Folder, error) {
	var created domain.Folder
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		folders, err := mb.Folders(ctx, false)
		if err != nil {
			return err
		}
		delimiter := domain.FolderDelimiter(folders)
		if err := domain.ValidateNewFolderName(name, delimiter); err != nil {
			return err
		}
		if _, exists := domain.FindFolder(folders, name); exists {
			return domain.ErrFolderExists
		}
		if err := mb.CreateFolder(ctx, name); err != nil {
			return err
		}
		created = domain.Folder{Name: name, Delimiter: delimiter, Selectable: true}
		return nil
	})
	if err != nil {
		return domain.Folder{}, err
	}
	s.logger.Info("webmail: carpeta creada", zap.String("username", sess.Username), zap.String("folder", name))
	return created, nil
}

// RenameFolder renombra una carpeta propia con sus subcarpetas. INBOX y las carpetas con papel
// no se renombran, ni un padre que contenga alguna de ellas.
func (s *Service) RenameFolder(ctx context.Context, sess domain.Session, name, newName string) (domain.Folder, error) {
	if err := domain.ValidateFolderName(name); err != nil {
		return domain.Folder{}, err
	}
	var renamed domain.Folder
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		folders, err := mb.Folders(ctx, false)
		if err != nil {
			return err
		}
		current, err := domain.CheckFolderChangeable(folders, name)
		if err != nil {
			return err
		}
		if err := domain.CheckRenameTarget(folders, current, newName); err != nil {
			return err
		}
		if err := mb.RenameFolder(ctx, current.Name, newName); err != nil {
			return err
		}
		renamed = domain.Folder{Name: newName, Delimiter: current.Delimiter, Selectable: current.Selectable}
		return nil
	})
	if err != nil {
		return domain.Folder{}, err
	}
	s.logger.Info("webmail: carpeta renombrada", zap.String("username", sess.Username), zap.String("folder", name), zap.String("new_name", newName))
	return renamed, nil
}

// DeleteFolder borra una carpeta propia sin subcarpetas, con sus mensajes. INBOX y las carpetas
// con papel no se borran.
func (s *Service) DeleteFolder(ctx context.Context, sess domain.Session, name string) error {
	if err := domain.ValidateFolderName(name); err != nil {
		return err
	}
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		folders, err := mb.Folders(ctx, false)
		if err != nil {
			return err
		}
		target, err := domain.CheckFolderChangeable(folders, name)
		if err != nil {
			return err
		}
		if len(domain.Descendants(folders, target)) > 0 {
			return domain.ErrFolderHasChildren
		}
		return mb.DeleteFolder(ctx, target.Name)
	})
	if err != nil {
		return err
	}
	s.logger.Info("webmail: carpeta borrada", zap.String("username", sess.Username), zap.String("folder", name))
	return nil
}

// EmptyFolder borra para siempre todos los mensajes de la papelera o del spam.
func (s *Service) EmptyFolder(ctx context.Context, sess domain.Session, name string) (int, error) {
	if err := domain.ValidateFolderName(name); err != nil {
		return 0, err
	}
	var removed int
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		folders, err := mb.Folders(ctx, false)
		if err != nil {
			return err
		}
		target, ok := domain.FindFolder(folders, name)
		if !ok {
			return domain.ErrFolderNotFound
		}
		if err := domain.CheckEmptiable(target); err != nil {
			return err
		}
		removed, err = mb.Empty(ctx, target.Name)
		return err
	})
	if err != nil {
		return 0, err
	}
	s.logger.Info("webmail: carpeta vaciada", zap.String("username", sess.Username), zap.String("folder", name), zap.Int("removed", removed))
	return removed, nil
}
