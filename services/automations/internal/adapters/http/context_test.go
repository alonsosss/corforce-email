package http

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/google/uuid"
)

func ctxFor(tenant uuid.UUID) context.Context {
	return middleware.WithTenantID(context.Background(), tenant.String())
}
