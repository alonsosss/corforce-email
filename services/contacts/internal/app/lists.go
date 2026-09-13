package app

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/segment"
	"github.com/google/uuid"
)

// MaxMembersPerRequest es el tope de contactos por alta de miembros.
const MaxMembersPerRequest = 1000

func normalizeName(raw string, invalid error) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > 200 {
		return "", invalid
	}
	return name, nil
}

func (uc *UseCase) CreateList(ctx context.Context, tenantID uuid.UUID, name, description string) (*domain.List, error) {
	name, err := normalizeName(name, domain.ErrInvalidListName)
	if err != nil {
		return nil, err
	}
	l := &domain.List{TenantID: tenantID, Name: name, Description: strings.TrimSpace(description)}
	if err := uc.lists.Create(ctx, l); err != nil {
		return nil, err
	}
	return l, nil
}

func (uc *UseCase) GetList(ctx context.Context, tenantID, id uuid.UUID) (*domain.List, error) {
	return uc.lists.Get(ctx, tenantID, id)
}

func (uc *UseCase) ListLists(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]domain.List, int64, error) {
	return uc.lists.List(ctx, tenantID, page, perPage)
}

func (uc *UseCase) UpdateList(ctx context.Context, tenantID, id uuid.UUID, name, description *string) (*domain.List, error) {
	l, err := uc.lists.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if name != nil {
		if l.Name, err = normalizeName(*name, domain.ErrInvalidListName); err != nil {
			return nil, err
		}
	}
	if description != nil {
		l.Description = strings.TrimSpace(*description)
	}
	if err := uc.lists.Update(ctx, l); err != nil {
		return nil, err
	}
	return l, nil
}

// DeleteList borra la lista y sus membresias. Si un segmento la usa, se rechaza: el
// segmento pasaria a seleccionar otra cosa (nadie con in_list, todos con not_in_list)
// sin que nadie lo hubiera decidido.
func (uc *UseCase) DeleteList(ctx context.Context, tenantID, id uuid.UUID) error {
	if _, err := uc.lists.Get(ctx, tenantID, id); err != nil {
		return err
	}
	used, err := uc.segmentUsing(ctx, tenantID, func(r segment.References) bool {
		for _, l := range r.Lists {
			if l == id.String() {
				return true
			}
		}
		return false
	})
	if err != nil {
		return err
	}
	if used {
		return domain.ErrListInUse
	}
	return uc.lists.Delete(ctx, tenantID, id)
}

// MembersResult dice cuantos contactos entraron y cuantos se ignoraron (ya estaban o no
// son contactos de la empresa).
type MembersResult struct {
	Added   int `json:"added"`
	Ignored int `json:"ignored"`
}

// RemovedMembersResult dice cuantos contactos salieron de la lista y cuantos se ignoraron
// (no estaban en ella).
type RemovedMembersResult struct {
	Removed int `json:"removed"`
	Ignored int `json:"ignored"`
}

func (uc *UseCase) AddMembers(ctx context.Context, tenantID, listID uuid.UUID, contactIDs []uuid.UUID) (*MembersResult, error) {
	if len(contactIDs) > MaxMembersPerRequest {
		return nil, domain.ErrTooManyMembers
	}
	ids := dedupeIDs(contactIDs)
	if _, err := uc.lists.Get(ctx, tenantID, listID); err != nil {
		return nil, err
	}
	added := 0
	if len(ids) > 0 {
		var err error
		if added, err = uc.lists.AddMembers(ctx, tenantID, listID, ids); err != nil {
			return nil, err
		}
	}
	return &MembersResult{Added: added, Ignored: len(contactIDs) - added}, nil
}

// RemoveMembers es la baja de miembros en bloque. Va por POST y no por DELETE: quitar
// contactos de una lista es editarla (lists/update), y el gateway exige a un DELETE un
// permiso de borrado del modulo que un rol que solo gestiona listas no tiene por que tener.
func (uc *UseCase) RemoveMembers(ctx context.Context, tenantID, listID uuid.UUID, contactIDs []uuid.UUID) (*RemovedMembersResult, error) {
	if len(contactIDs) > MaxMembersPerRequest {
		return nil, domain.ErrTooManyMembers
	}
	ids := dedupeIDs(contactIDs)
	if _, err := uc.lists.Get(ctx, tenantID, listID); err != nil {
		return nil, err
	}
	removed := 0
	if len(ids) > 0 {
		var err error
		if removed, err = uc.lists.RemoveMembers(ctx, tenantID, listID, ids); err != nil {
			return nil, err
		}
	}
	return &RemovedMembersResult{Removed: removed, Ignored: len(contactIDs) - removed}, nil
}
