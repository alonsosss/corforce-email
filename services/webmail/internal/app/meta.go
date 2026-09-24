package app

import (
	"context"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

// Meta son los topes y catalogos que la interfaz necesita para validar antes de enviar.
// Salen de las constantes del dominio y de la configuracion que el servicio aplica: la
// interfaz deja de copiarlos y no puede quedarse desfasada.
type Meta struct {
	MaxRecipients      int
	MaxMessageBytes    int64
	MaxAttachments     int
	MaxDownloadBytes   int64
	MaxBodyPartBytes   int64
	MaxSubjectChars    int
	MaxSearchBytes     int
	MaxFolderNameBytes int
	MaxBatchUIDs       int
	MaxScheduledDays   int
	MaxReminderDays    int
	MaxImportBytes     int64
	MaxThreadMessages  int
	InboxCategories    []domain.Category
	DefaultPageSize    int
	MaxPageSize        int
	FolderRoles        []domain.FolderRole
	MutableFlags       []domain.Flag
	SessionIdle        time.Duration
	SessionMax         time.Duration
}

func (s *Service) Meta() Meta {
	return Meta{
		MaxRecipients:      s.cfg.Limits.MaxRecipients,
		MaxMessageBytes:    s.cfg.Limits.MaxMessageBytes,
		MaxAttachments:     domain.MaxAttachments,
		MaxDownloadBytes:   s.cfg.MaxAttachmentBytes,
		MaxBodyPartBytes:   s.cfg.MaxBodyPartBytes,
		MaxSubjectChars:    domain.MaxSubjectRunes,
		MaxSearchBytes:     domain.MaxSearchBytes,
		MaxFolderNameBytes: domain.MaxFolderNameBytes,
		MaxBatchUIDs:       domain.MaxBatchUIDs,
		MaxScheduledDays:   s.cfg.MaxScheduledDays,
		MaxReminderDays:    s.cfg.MaxReminderDays,
		MaxImportBytes:     s.cfg.MaxImportBytes,
		MaxThreadMessages:  domain.MaxThreadMessages,
		InboxCategories:    append([]domain.Category(nil), domain.Categories...),
		DefaultPageSize:    domain.DefaultPerPage,
		MaxPageSize:        domain.MaxPerPage,
		FolderRoles:        append([]domain.FolderRole(nil), domain.SpecialRoles...),
		MutableFlags:       append([]domain.Flag(nil), domain.MutableFlags...),
		SessionIdle:        s.cfg.Sessions.Idle,
		SessionMax:         s.cfg.Sessions.Max,
	}
}

// Identities son los remitentes del buzon para el selector: el propio buzon primero y
// despues las direcciones concretas que el directorio de la celda le permite con la regla
// de Postfix. Son las mismas que Send admite.
func (s *Service) Identities(ctx context.Context, sess domain.Session) ([]domain.SenderIdentity, error) {
	addresses, err := s.directory.SenderIdentities(ctx, sess.Username)
	if err != nil {
		s.logger.Error("webmail: no se pudieron leer los remitentes del buzon", zap.String("username", sess.Username), zap.Error(err))
		return nil, unavailable(err)
	}
	out := []domain.SenderIdentity{{Address: sess.Username, Name: sess.DisplayName, Primary: true}}
	seen := map[string]bool{sess.Username: true}
	for _, a := range addresses {
		a = strings.ToLower(a)
		if seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, domain.SenderIdentity{Address: a})
	}
	return out, nil
}
