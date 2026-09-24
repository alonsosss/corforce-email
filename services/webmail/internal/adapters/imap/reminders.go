package imap

import (
	"context"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	imaplib "github.com/emersion/go-imap/v2"
)

// MoveTracked mueve un mensaje y devuelve su UID en el destino a partir del COPYUID de la respuesta
// (UIDPLUS, que Dovecot anuncia). Sin el, el UID queda en cero y quien llama lo busca por Message-ID.
func (m *mailbox) MoveTracked(ctx context.Context, folder string, uid uint32, dest string) (domain.AppendedMessage, error) {
	defer m.watch(ctx)()
	if err := m.selectFolder(folder, true); err != nil {
		return domain.AppendedMessage{}, err
	}
	set, n, err := m.existing([]uint32{uid})
	if err != nil {
		return domain.AppendedMessage{}, err
	}
	if n == 0 {
		return domain.AppendedMessage{}, domain.ErrMessageNotFound
	}
	data, err := m.c.Move(set, dest).Wait()
	if err != nil {
		return domain.AppendedMessage{}, mapError(err, domain.ErrFolderNotFound)
	}
	out := domain.AppendedMessage{UIDValidity: data.UIDValidity}
	if dest, ok := data.DestUIDs.(imaplib.UIDSet); ok {
		if uids, ok := dest.Nums(); ok && len(uids) == 1 {
			out.UID = uint32(uids[0])
		}
	}
	if out.UID == 0 {
		out.UIDValidity = 0
	}
	return out, nil
}

// HasReply busca en la carpeta un mensaje que cite el Message-ID en In-Reply-To o References. Dovecot
// busca la cabecera como subcadena, asi que se busca con los corchetes: <id> no casa con <xid>.
func (m *mailbox) HasReply(ctx context.Context, folder, messageID string) (bool, error) {
	defer m.watch(ctx)()
	if err := m.selectFolder(folder, false); err != nil {
		return false, err
	}
	ref := "<" + messageID + ">"
	criteria := &imaplib.SearchCriteria{Or: [][2]imaplib.SearchCriteria{{
		{Header: []imaplib.SearchCriteriaHeaderField{{Key: "In-Reply-To", Value: ref}}},
		{Header: []imaplib.SearchCriteriaHeaderField{{Key: "References", Value: ref}}},
	}}}
	data, err := m.c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return false, mapError(err, nil)
	}
	return len(data.AllUIDs()) > 0, nil
}
