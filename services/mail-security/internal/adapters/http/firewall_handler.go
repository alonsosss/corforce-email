package http

import (
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

// platformFrom dice si quien llama opera la plataforma. HasAnyRole sin roles adicionales
// solo deja pasar al superadmin: el mismo criterio que las rutas de plataforma del
// directorio de correo.
func platformFrom(r *http.Request) bool { return middleware.HasAnyRole(r.Context()) }

// ── Cortafuegos de la celda ───────────────────────────────────────────────────

func (h *Handler) ListFirewallNetworks(w http.ResponseWriter, r *http.Request) {
	out, err := h.firewall.ListNetworks(r.Context(), platformFrom(r))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

// AddFirewallNetwork: {"list": "allow|deny", "network": "IP o CIDR", "note": "..."}.
func (h *Handler) AddFirewallNetwork(w http.ResponseWriter, r *http.Request) {
	var body struct {
		List    domain.FirewallList `json:"list"`
		Network string              `json:"network"`
		Note    string              `json:"note"`
	}
	if !decode(w, r, &body) {
		return
	}
	out, err := h.firewall.AddNetwork(r.Context(), platformFrom(r), body.List, body.Network, body.Note)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, out)
}

func (h *Handler) DeleteFirewallNetwork(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.firewall.DeleteNetwork(r.Context(), platformFrom(r), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) GetFirewallOptions(w http.ResponseWriter, r *http.Request) {
	out, err := h.firewall.Options(r.Context(), platformFrom(r))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) PutFirewallOptions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BanTime          int  `json:"ban_time"`
		MaxBanTime       int  `json:"max_ban_time"`
		BanTimeIncrement bool `json:"ban_time_increment"`
		MaxAttempts      int  `json:"max_attempts"`
		RetryWindow      int  `json:"retry_window"`
		NetbanIPv4       int  `json:"netban_ipv4"`
		NetbanIPv6       int  `json:"netban_ipv6"`
	}
	if !decode(w, r, &body) {
		return
	}
	out, err := h.firewall.PutOptions(r.Context(), platformFrom(r), domain.FirewallOptions{
		BanTime: body.BanTime, MaxBanTime: body.MaxBanTime, BanTimeIncrement: body.BanTimeIncrement,
		MaxAttempts: body.MaxAttempts, RetryWindow: body.RetryWindow, NetbanIPv4: body.NetbanIPv4, NetbanIPv6: body.NetbanIPv6,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) ListFirewallBans(w http.ResponseWriter, r *http.Request) {
	out, err := h.firewall.Bans(r.Context(), platformFrom(r))
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, out)
}

// UnbanFirewallNetwork: {"network": "203.0.113.0/24"}, tal como aparece en los baneos. 202:
// netfilter lo levanta en su siguiente vuelta.
func (h *Handler) UnbanFirewallNetwork(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Network string `json:"network"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := h.firewall.Unban(r.Context(), platformFrom(r), body.Network); err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}
