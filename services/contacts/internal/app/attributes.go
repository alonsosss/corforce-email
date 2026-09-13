package app

import (
	"context"
	"strings"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/segment"
	"github.com/google/uuid"
)

type CreateAttributeInput struct {
	Key      string
	Type     string
	Label    string
	Required bool
}

func (uc *UseCase) ListAttributes(ctx context.Context, tenantID uuid.UUID) ([]domain.AttributeDefinition, error) {
	return uc.attributes.List(ctx, tenantID)
}

// CreateAttribute declara un atributo. El tipo queda fijo: cambiarlo dejaria valores ya
// guardados de otro tipo y segmentos que comparan con el anterior.
func (uc *UseCase) CreateAttribute(ctx context.Context, tenantID uuid.UUID, in CreateAttributeInput) (*domain.AttributeDefinition, error) {
	key := strings.TrimSpace(in.Key)
	if err := domain.ValidateAttributeKey(key); err != nil {
		return nil, err
	}
	typ, err := domain.ParseAttrType(in.Type)
	if err != nil {
		return nil, err
	}
	existing, err := uc.attributes.List(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if len(existing) >= domain.MaxAttributeDefinitions {
		return nil, domain.ErrTooManyAttributes
	}
	d := &domain.AttributeDefinition{
		TenantID: tenantID, Key: key, Type: typ, Label: strings.TrimSpace(in.Label), Required: in.Required,
	}
	if err := uc.attributes.Create(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

// UpdateAttribute cambia la etiqueta o si es obligatorio. Obligatorio rige para los
// contactos que se creen desde ahora; los existentes no se invalidan.
func (uc *UseCase) UpdateAttribute(ctx context.Context, tenantID uuid.UUID, key string, label *string, required *bool) (*domain.AttributeDefinition, error) {
	d, err := uc.attributes.Get(ctx, tenantID, key)
	if err != nil {
		return nil, err
	}
	if label != nil {
		d.Label = strings.TrimSpace(*label)
	}
	if required != nil {
		d.Required = *required
	}
	if err := uc.attributes.Update(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

// DeleteAttribute retira el atributo y quita su valor de todos los contactos, para que
// una clave redeclarada despues con otro tipo no herede valores del anterior. No se
// retira si un segmento lo usa.
func (uc *UseCase) DeleteAttribute(ctx context.Context, tenantID uuid.UUID, key string) error {
	if _, err := uc.attributes.Get(ctx, tenantID, key); err != nil {
		return err
	}
	used, err := uc.segmentUsing(ctx, tenantID, func(r segment.References) bool {
		for _, a := range r.Attributes {
			if a == key {
				return true
			}
		}
		return false
	})
	if err != nil {
		return err
	}
	if used {
		return domain.ErrAttributeInUse
	}
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		if err := uc.attributes.Delete(ctx, tenantID, key); err != nil {
			return err
		}
		return uc.contacts.StripAttribute(ctx, tenantID, key)
	})
}
