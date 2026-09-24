package middleware

import (
	"context"
	"strings"
)

// Una peticion autenticada con clave de API no lleva usuario ni roles: el gateway inyecta la
// empresa, el id de la clave y su alcance efectivo, ya acotado por access-control a lo que hoy
// tiene quien la creo. Los servicios la reconocen por HeaderAPIKeyID y la autorizan solo con
// ese alcance (pkg/authz), nunca con la politica de una persona.
const (
	HeaderAPIKeyID     = "X-Api-Key-ID"
	HeaderAPIKeyScopes = "X-Api-Key-Scopes"
)

const (
	CtxAPIKeyID     contextKey = "api_key_id"
	CtxAPIKeyScopes contextKey = "api_key_scopes"
)

// APIKeyScope es un permiso (module, resource, action) del alcance de una clave. Sin comodines:
// una clave solo recibe permisos concretos del catalogo.
type APIKeyScope struct {
	Module   string `json:"module"`
	Resource string `json:"resource"`
	Action   string `json:"action"`
}

func (s APIKeyScope) String() string {
	return s.Module + ":" + s.Resource + ":" + s.Action
}

// validScopePart admite lo que el catalogo de permisos usa: minusculas, digitos y '_'.
func validScopePart(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// ParseAPIKeyScopes lee la lista "module:resource:action,..." de HeaderAPIKeyScopes. Una
// entrada mal formada invalida la lista entera: un alcance a medias no se concede.
func ParseAPIKeyScopes(raw string) ([]APIKeyScope, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true
	}
	parts := strings.Split(raw, ",")
	out := make([]APIKeyScope, 0, len(parts))
	for _, p := range parts {
		f := strings.Split(strings.TrimSpace(p), ":")
		if len(f) != 3 || !validScopePart(f[0]) || !validScopePart(f[1]) || !validScopePart(f[2]) {
			return nil, false
		}
		out = append(out, APIKeyScope{Module: f[0], Resource: f[1], Action: f[2]})
	}
	return out, true
}

// FormatAPIKeyScopes es la inversa de ParseAPIKeyScopes.
func FormatAPIKeyScopes(scopes []APIKeyScope) string {
	parts := make([]string, len(scopes))
	for i, s := range scopes {
		parts[i] = s.String()
	}
	return strings.Join(parts, ",")
}

// WithAPIKey marca el contexto como autenticado por una clave con su alcance.
func WithAPIKey(ctx context.Context, keyID string, scopes []APIKeyScope) context.Context {
	ctx = context.WithValue(ctx, CtxAPIKeyID, keyID)
	return context.WithValue(ctx, CtxAPIKeyScopes, scopes)
}

// GetAPIKeyID devuelve el id de la clave de la peticion, vacio si no la autentico una clave.
func GetAPIKeyID(ctx context.Context) string {
	v, _ := ctx.Value(CtxAPIKeyID).(string)
	return v
}

// GetAPIKeyScopes devuelve el alcance efectivo de la clave de la peticion.
func GetAPIKeyScopes(ctx context.Context) []APIKeyScope {
	v, _ := ctx.Value(CtxAPIKeyScopes).([]APIKeyScope)
	return v
}

// APIKeyAllows dice si el alcance de la clave de la peticion incluye el permiso exacto.
func APIKeyAllows(ctx context.Context, module, resource, action string) bool {
	for _, s := range GetAPIKeyScopes(ctx) {
		if s.Module == module && s.Resource == resource && s.Action == action {
			return true
		}
	}
	return false
}
