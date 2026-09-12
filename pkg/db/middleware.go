package db

import (
	"context"
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TenantPoolMiddleware resuelve el pool de la empresa de la peticion y lo deja en
// el contexto via WithPool. Debe ir DETRAS de InjectFromGateway (la empresa sale
// del contexto).
func TenantPoolMiddleware(tdb *TenantDB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenantID := middleware.GetTenantID(r.Context())
			if tenantID == "" {
				response.ErrUnauthorized(w, "missing tenant")
				return
			}
			pool, err := tdb.ResolveForTenant(r.Context(), tenantID)
			if err != nil {
				response.ErrNotFound(w, "tenant database not found")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPool(r.Context(), pool)))
		})
	}
}

// TenantHeaderPoolMiddleware resuelve el pool a partir de la cabecera
// X-Tenant-ID, para rutas servicio-a-servicio donde no hay JWT del que derivar
// la empresa. Debe ir SIEMPRE detras de una comprobacion de token interno: por si
// sola, la cabecera es un dato que cualquiera puede escribir.
func TenantHeaderPoolMiddleware(tdb *TenantDB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenantID := r.Header.Get("X-Tenant-ID")
			if tenantID == "" {
				response.ErrUnauthorized(w, "missing tenant")
				return
			}
			pool, err := tdb.ResolveForTenant(r.Context(), tenantID)
			if err != nil {
				response.ErrNotFound(w, "tenant database not found")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPool(r.Context(), pool)))
		})
	}
}

// StaticPoolMiddleware deja en el contexto un pool fijo: el de la base de la celda en los
// servicios que viven en ella. La empresa sigue viajando en el contexto (InjectFromGateway)
// y es lo que leen las politicas RLS al abrir la transaccion.
func StaticPoolMiddleware(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(WithPool(r.Context(), pool)))
		})
	}
}

// sessionFlags resuelve la identidad y el indicador que leen las politicas RLS.
//
//	app.current_user_id   -> quien actua; vacio en trabajos de fondo.
//	app.current_tenant_id -> la empresa; una politica sin este valor no deja pasar nada.
//	app.is_privileged     -> "tiene autoridad administrativa para escribir?", con el
//	                         mismo criterio que middleware.IsPrivileged, unica fuente
//	                         de verdad de esa pregunta en el resto del codigo.
//
// Una politica que autorice una escritura debe mirar app.is_privileged.
func sessionFlags(ctx context.Context) (userID, tenantID, isPrivileged string) {
	userID = middleware.GetUserID(ctx)
	tenantID = middleware.GetTenantID(ctx)
	isPrivileged = "false"
	if middleware.IsPrivileged(ctx) {
		isPrivileged = "true"
	}
	return userID, tenantID, isPrivileged
}
