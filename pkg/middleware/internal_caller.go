package middleware

import "net/http"

// RequireInternalCaller cierra una ruta a las personas: solo la llaman otros servicios de
// la plataforma, ya autenticados por el token interno (RequireGatewayToken, montado antes).
// El gateway inyecta siempre el usuario de la sesion y no enruta /internal, asi que una
// peticion con usuario (o con clave de API de una empresa) es alguien intentando hacer por su
// cuenta lo que solo hace la plataforma. Va despues de InjectFromGateway.
func RequireInternalCaller(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if GetUserID(r.Context()) != "" || GetAPIKeyID(r.Context()) != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"FORBIDDEN","message":"ruta reservada a los servicios de la plataforma"}}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
