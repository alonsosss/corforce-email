package app

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// SearchAddressBook busca companeros de la empresa del buzon con el que se abrio la sesion. El buzon
// sale de la sesion, nunca de la peticion, asi que nadie consulta la libreta de otra empresa. El
// tope de resultados y la validacion del texto son de mail-directory.
func (s *Service) SearchAddressBook(ctx context.Context, sess domain.Session, query string, limit int) ([]domain.AddressBookEntry, error) {
	entries, err := s.addressBook.Search(ctx, sess.Username, query, limit)
	if err != nil {
		var verr *domain.ValidationError
		if errors.As(err, &verr) {
			return nil, err
		}
		s.logFailure("no se pudo consultar la libreta de direcciones", sess, err)
		return nil, unavailable(err)
	}
	return entries, nil
}
