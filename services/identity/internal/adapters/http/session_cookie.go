package http

import (
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
)

// El refresh token no debe estar al alcance de JavaScript.
//
// Guardado en localStorage, cualquier XSS lo lee y se lleva una sesion de dias: el
// atacante ya no necesita mantener la pagina abierta ni volver a robar nada. En una cookie
// HttpOnly el navegador lo envia solo, el script no puede leerlo, y el robo se limita al
// access token de minutos que vive en memoria de la pestana.
//
// Path acotado a /api/v1/auth: la cookie no viaja en ninguna peticion de negocio, solo en
// renovar y cerrar sesion. SameSite=Strict remata la defensa CSRF; las peticiones de
// negocio siguen autenticandose con la cabecera Authorization, que ningun sitio ajeno
// puede fabricar.
const (
	refreshCookieName = "cf_rt"
	refreshCookiePath = "/api/v1/auth"
)

// cookieAuthRequested indica que el cliente es un navegador que quiere el refresh token en
// cookie. Se pide explicitamente (cabecera o campo del JSON) para no romper a los
// clientes que no son navegador, que siguen recibiendolo en el JSON.
func cookieAuthRequested(r *http.Request, bodyFlag bool) bool {
	return bodyFlag || r.Header.Get("X-Auth-Mode") == "cookie"
}

func refreshCookieSecure() bool {
	// Segura por defecto: hay que APAGARLA a proposito para desarrollo local, no
	// encenderla para produccion. Atarlo a ENVIRONMENT es fragil: un despliegue real que
	// corra con ENVIRONMENT=development emitiria la cookie sin Secure sin que nadie lo
	// notara.
	if v := os.Getenv("AUTH_COOKIE_SECURE"); v != "" {
		if secure, err := strconv.ParseBool(v); err == nil {
			return secure
		}
	}
	return true
}

func setRefreshCookie(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Value:    token,
		Path:     refreshCookiePath,
		HttpOnly: true,
		Secure:   refreshCookieSecure(),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(ttl.Seconds()),
	})
}

func clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Value:    "",
		Path:     refreshCookiePath,
		HttpOnly: true,
		Secure:   refreshCookieSecure(),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

func refreshCookieValue(r *http.Request) string {
	c, err := r.Cookie(refreshCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// issueSession entrega el par de tokens al cliente. En modo cookie, el refresh viaja como
// cookie HttpOnly y se OMITE del JSON: si siguiera ahi, un XSS podria leerlo de la
// respuesta y toda la proteccion seria decorativa.
func (h *Handler) issueSession(w http.ResponseWriter, r *http.Request, res *ports.LoginResponse, cookieMode bool) *ports.LoginResponse {
	if res == nil || !cookieAuthRequested(r, cookieMode) {
		return res
	}
	if res.RefreshToken != "" {
		setRefreshCookie(w, res.RefreshToken, h.refreshCookieTTL)
	}
	clone := *res
	clone.RefreshToken = ""
	return &clone
}
