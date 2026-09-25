package http

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/pkg/validate"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// La sesion del webmail no vive en el navegador: un token opaco de 256 bits en una cookie
// HttpOnly (ningun script la lee), SameSite=Strict (no viaja en peticiones de otro sitio)
// y Path acotado al API del webmail (no viaja al resto de la plataforma). En el almacen
// solo esta su hash.
//
// El segundo paso del inicio de sesion usa otra cookie con las mismas protecciones, acotada a las
// rutas de la sesion y a la vida del desafio: solo lleva el token del desafio, que no abre el buzon.
const (
	cookieName    = "cf_wm"
	mfaCookieName = "cf_wm_mfa"
	maxLoginBody  = 8 << 10
)

// mfaCookiePath limita la cookie del desafio a POST /session y POST /session/mfa.
const mfaCookiePath = BasePath + "/session"

type sessionContextKey struct{}

func sessionFrom(r *http.Request) domain.Session {
	s, _ := r.Context().Value(sessionContextKey{}).(domain.Session)
	return s
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Login responde exactamente igual para un buzon inexistente y una contrasena mala:
// mismo codigo, mismo cuerpo y sin cookie.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxLoginBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	res, err := h.app.Login(ctx, req.Username, req.Password, clientIP(r), cookieValue(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if res.MFAChallenge != "" {
		h.setMFACookie(w, res.MFAChallenge)
		response.JSON(w, http.StatusOK, mfaRequiredDTO{MFARequired: true})
		return
	}
	h.clearMFACookie(w, r)
	h.setCookie(w, res.Token)
	response.JSON(w, http.StatusOK, toSessionDTO(res.Session, h.cfg, nil))
}

type mfaRequiredDTO struct {
	MFARequired bool `json:"mfa_required"`
}

type mfaLoginRequest struct {
	Code string `json:"code"`
}

// LoginMFA es el segundo paso: con el desafio de la cookie cf_wm_mfa y un codigo valido abre la
// sesion y responde lo mismo que POST /session sin verificacion en dos pasos. Un desafio caducado,
// agotado o sin cookie es 401 MFA_CHALLENGE_EXPIRED y borra la cookie del desafio.
func (h *Handler) LoginMFA(w http.ResponseWriter, r *http.Request) {
	var req mfaLoginRequest
	if err := validate.DecodeJSONLimit(w, r, &req, maxLoginBody); err != nil {
		response.ErrBadRequest(w, err.Error())
		return
	}
	challenge := mfaCookieValue(r)
	if challenge == "" {
		writeError(w, domain.ErrMFAChallengeExpired)
		return
	}
	ctx, cancel := h.opContext(r)
	defer cancel()
	token, sess, err := h.app.CompleteLogin(ctx, challenge, req.Code, clientIP(r), cookieValue(r))
	if err != nil {
		if errors.Is(err, domain.ErrMFAChallengeExpired) {
			h.clearMFACookie(w, r)
		}
		h.fail(w, r, err)
		return
	}
	h.clearMFACookie(w, r)
	h.setCookie(w, token)
	response.JSON(w, http.StatusOK, toSessionDTO(sess, h.cfg, nil))
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	if err := h.app.Logout(ctx, cookieValue(r)); err != nil {
		h.fail(w, r, err)
		return
	}
	h.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Session(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := h.opContext(r)
	defer cancel()
	sess := sessionFrom(r)
	response.JSON(w, http.StatusOK, toSessionDTO(sess, h.cfg, h.app.Quota(ctx, sess)))
}

// requireSession resuelve la cookie a una sesion viva o responde 401 y borra la cookie.
func (h *Handler) requireSession(next http.Handler) http.Handler {
	return h.sessionMiddleware(next, h.app.Authenticate)
}

// sessionMiddleware valida la cookie con authenticate (con o sin renovar la inactividad) y deja la sesion en
// el contexto.
func (h *Handler) sessionMiddleware(next http.Handler, authenticate func(context.Context, string) (domain.Session, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sin sesion no hay buzon al que cargar la peticion: cuenta contra el cupo de su IP.
		token := cookieValue(r)
		if token == "" {
			if !h.overIPLimit(w, r) {
				writeError(w, domain.ErrSessionInvalid)
			}
			return
		}
		ctx, cancel := h.opContext(r)
		sess, err := authenticate(ctx, token)
		cancel()
		if err != nil {
			if errors.Is(err, domain.ErrSessionInvalid) && h.overIPLimit(w, r) {
				return
			}
			h.fail(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, sess)))
	})
}

func (h *Handler) setCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     BasePath,
		HttpOnly: true,
		Secure:   h.cfg.CookieSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(h.cfg.SessionMax.Seconds()),
	})
}

func (h *Handler) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     BasePath,
		HttpOnly: true,
		Secure:   h.cfg.CookieSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

func (h *Handler) setMFACookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     mfaCookieName,
		Value:    token,
		Path:     mfaCookiePath,
		HttpOnly: true,
		Secure:   h.cfg.CookieSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(h.cfg.MFAChallengeTTL.Seconds()),
	})
}

// clearMFACookie borra la cookie del desafio si el navegador la trae.
func (h *Handler) clearMFACookie(w http.ResponseWriter, r *http.Request) {
	if mfaCookieValue(r) == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     mfaCookieName,
		Value:    "",
		Path:     mfaCookiePath,
		HttpOnly: true,
		Secure:   h.cfg.CookieSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

func mfaCookieValue(r *http.Request) string {
	c, err := r.Cookie(mfaCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

func cookieValue(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// clientIP es la IP real del visitante. La pone el gateway en X-Real-IP despues de
// validarla contra el proxy de borde; el servicio solo acepta peticiones con el token del
// gateway, asi que el cliente no puede fijarla. Llega a mail-auth para el freno de
// fuerza bruta por (buzon, IP).
func clientIP(r *http.Request) string {
	if ip := net.ParseIP(r.Header.Get("X-Real-IP")); ip != nil {
		return ip.String()
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
