package middleware

import (
	"context"
	"net/http"
)

// Roles del sistema. Son estructurales, no de negocio: existen en toda empresa,
// los siembra access-control con is_system = true (tenant_admin en el alta de cada
// empresa, a peticion de organization) y no se pueden borrar. Que
// permiso concreto tiene cada rol es un dato de access_control, nunca una lista
// en el codigo; aqui solo se distingue quien opera la plataforma de quien
// administra su empresa, porque ambos existen antes de que haya ningun permiso.
const (
	// RoleSuperadmin es la cuenta de plataforma: opera todas las empresas y no
	// dirige ninguna. Nunca recibe los avisos internos de un cliente.
	RoleSuperadmin = "superadmin"
	// RoleTenantAdmin administra su propia empresa: usuarios, dominios, politicas.
	RoleTenantAdmin = "tenant_admin"
)

// TenantAuthorityRoles son los roles con autoridad administrativa DE LA EMPRESA,
// para cuando hay que RESOLVER a esas personas y no solo comprobar al que llama
// (por ejemplo, a quien sube un aviso que nadie atendio). Deja fuera al
// superadmin a proposito.
func TenantAuthorityRoles() []string {
	return []string{RoleTenantAdmin}
}

// IsPrivileged indica si el usuario del contexto tiene autoridad administrativa:
// es el criterio unico que leen las politicas RLS de escritura (app.is_privileged)
// y los handlers que reservan una accion al administrador.
func IsPrivileged(ctx context.Context) bool {
	for _, role := range GetRoles(ctx) {
		if role == RoleSuperadmin || role == RoleTenantAdmin {
			return true
		}
	}
	return false
}

// IsSuperadmin indica si el usuario del contexto es un operador de plataforma. Los roles
// los escribe solo el gateway.
func IsSuperadmin(ctx context.Context) bool {
	for _, role := range GetRoles(ctx) {
		if role == RoleSuperadmin {
			return true
		}
	}
	return false
}

// HasAnyRole indica si el usuario del contexto tiene alguno de los roles dados.
// El superadmin de plataforma siempre pasa, igual que en RequireRoles.
func HasAnyRole(ctx context.Context, roles ...string) bool {
	allowed := make(map[string]bool, len(roles)+1)
	allowed[RoleSuperadmin] = true
	for _, r := range roles {
		allowed[r] = true
	}
	for _, role := range GetRoles(ctx) {
		if allowed[role] {
			return true
		}
	}
	return false
}

// RequireRoles exige que el usuario tenga alguno de los roles indicados (defensa
// en profundidad: los endpoints sensibles validan el rol EN EL SERVICIO, sin
// depender solo del gateo por modulo del gateway). El superadmin siempre pasa.
// Los roles llegan en el contexto via InjectFromGateway (cabecera X-User-Roles,
// que solo el gateway puede escribir gracias a X-Gateway-Token).
func RequireRoles(roles ...string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(roles)+1)
	allowed[RoleSuperadmin] = true
	for _, r := range roles {
		allowed[r] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, role := range GetRoles(r.Context()) {
				if allowed[role] {
					next.ServeHTTP(w, r)
					return
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"FORBIDDEN","message":"su rol no permite esta operación"}}`))
		})
	}
}
