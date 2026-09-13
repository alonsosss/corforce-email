package http

import (
	"net"
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/middleware"
)

// EngineNetworkGuard acota los listeners de los motores a la red interna de la celda.
// No hay gateway ni JWT delante: la unica autenticacion es la IP de origen, asi que se
// comprueba en cada peticion contra MAIL_ENGINE_ALLOWED_CIDRS (por defecto RFC1918 y
// loopback, el mismo conjunto que pkg/middleware usa para los proxies de confianza).
func EngineNetworkGuard(spec string) func(http.Handler) http.Handler {
	allowed := middleware.TrustedProxyCIDRs(spec)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				host = r.RemoteAddr
			}
			ip := net.ParseIP(host)
			if ip == nil || !ipAllowed(ip, allowed) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func ipAllowed(ip net.IP, allowed []*net.IPNet) bool {
	for _, cidr := range allowed {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}
