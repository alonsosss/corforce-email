package app

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// secretoTOTP es un secreto base32 valido de 20 bytes.
const secretoTOTP = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"

// activar deja al buzon con la verificacion activa: el codigo 111111 vale en el paso 100.
func activar(t *testing.T, h *harness, username string) []string {
	t.Helper()
	h.totp.codes["111111"] = 100
	codes, err := h.uc.ActivateMFAByUsername(context.Background(), username, secretoTOTP, "111111")
	if err != nil {
		t.Fatal(err)
	}
	return codes
}

func TestActivarLaVerificacionEnDosPasos(t *testing.T) {
	h := newHarness()
	m := h.addMailbox(uuid.New(), "ana@acme.test", 0)
	ctx := context.Background()

	if _, err := h.uc.ActivateMFAByUsername(ctx, "ana@acme.test", "no es base32", "111111"); fieldOf(t, err) != "secret" {
		t.Fatal("un secreto ilegible se senala en su campo")
	}
	if _, err := h.uc.ActivateMFAByUsername(ctx, "ana@acme.test", "JBSWY3DP", "111111"); fieldOf(t, err) != "secret" {
		t.Fatal("un secreto de menos de 16 bytes no se admite")
	}
	for _, code := range []string{"", "12345", "abcdef", "1234567"} {
		if _, err := h.uc.ActivateMFAByUsername(ctx, "ana@acme.test", secretoTOTP, code); !errors.Is(err, domain.ErrInvalidMFACode) {
			t.Fatalf("%q: %v", code, err)
		}
	}
	if _, err := h.uc.ActivateMFAByUsername(ctx, "ana@acme.test", secretoTOTP, "222222"); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("codigo que no coincide: %v", err)
	}
	if len(h.mfa.rows) != 0 || m.MFAEnabled || h.published("mail.mailbox.mfa_enabled") != 0 {
		t.Fatal("un intento fallido no guarda nada")
	}

	h.totp.codes["111111"] = 100
	codes, err := h.uc.ActivateMFAByUsername(ctx, "ana@acme.test", " "+strings.ToLower(secretoTOTP)+" ", "111 111")
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != domain.RecoveryCodeCount {
		t.Fatalf("%d codigos", len(codes))
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != domain.RecoveryCodeLength+1 || c[5] != '-' || !domain.IsRecoveryCode(domain.NormalizeMFACode(c)) || seen[c] {
			t.Fatalf("codigo %q", c)
		}
		seen[c] = true
	}
	row := h.mfa.rows[m.ID]
	if row == nil || row.TenantID != m.TenantID || row.LastStep != 100 || len(row.RecoveryHashes) != domain.RecoveryCodeCount {
		t.Fatalf("fila: %+v", row)
	}
	if !bytes.Equal(row.SecretEnc, append(append(m.ID[:], ':'), secretoTOTP...)) {
		t.Fatalf("el secreto se guarda sellado con el id del buzon y normalizado: %q", row.SecretEnc)
	}
	for _, c := range codes {
		if slices.Contains(row.RecoveryHashes, c) || slices.Contains(row.RecoveryHashes, domain.NormalizeMFACode(c)) {
			t.Fatal("los codigos no se guardan en claro")
		}
	}
	if !m.MFAEnabled || h.published("mail.mailbox.mfa_enabled") != 1 || len(h.events.outside) != 0 {
		t.Fatalf("buzon y evento: %+v %v", m, h.events.subjects)
	}
	if _, err := h.uc.ActivateMFAByUsername(ctx, "ana@acme.test", secretoTOTP, "111111"); !errors.Is(err, domain.ErrMFAAlreadyEnabled) {
		t.Fatalf("activar dos veces: %v", err)
	}
	st, err := h.uc.MFAStatusByUsername(ctx, "ana@acme.test")
	if err != nil || !st.Enabled || st.EnabledAt == nil || st.RecoveryRemaining != domain.RecoveryCodeCount {
		t.Fatalf("estado: %+v %v", st, err)
	}
}

func TestUnCodigoTOTPValeUnaSolaVez(t *testing.T) {
	h := newHarness()
	m := h.addMailbox(uuid.New(), "ana@acme.test", 0)
	ctx := context.Background()
	activar(t, h, "ana@acme.test")

	if _, err := h.uc.VerifyMFAByUsername(ctx, "ana@acme.test", "111111"); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("el codigo de la activacion no vale como segundo paso: %v", err)
	}
	h.totp.codes["099999"] = 99
	if _, err := h.uc.VerifyMFAByUsername(ctx, "ana@acme.test", "099999"); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("un paso anterior al ultimo usado: %v", err)
	}
	h.totp.codes["333333"] = 101
	v, err := h.uc.VerifyMFAByUsername(ctx, "ana@acme.test", " 333 333 ")
	if err != nil || v.Method != domain.MFAMethodTOTP || v.RecoveryRemaining != domain.RecoveryCodeCount {
		t.Fatalf("verificar: %+v %v", v, err)
	}
	if h.mfa.rows[m.ID].LastStep != 101 {
		t.Fatalf("paso guardado: %d", h.mfa.rows[m.ID].LastStep)
	}
	if _, err := h.uc.VerifyMFAByUsername(ctx, "ana@acme.test", "333333"); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("repetido: %v", err)
	}
	if got := h.totp.secrets[len(h.totp.secrets)-1]; got != secretoTOTP {
		t.Fatalf("se valida con el secreto abierto: %q", got)
	}
	asked := len(h.totp.secrets)
	if _, err := h.uc.VerifyMFAByUsername(ctx, "ana@acme.test", "no-es-un-codigo"); !errors.Is(err, domain.ErrInvalidMFACode) || len(h.totp.secrets) != asked {
		t.Fatalf("lo que no tiene forma de codigo ni se consulta: %v", err)
	}
	if _, err := h.uc.VerifyMFAByUsername(ctx, "nadie@acme.test", "333333"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("buzon desconocido: %v", err)
	}
}

func TestLosCodigosDeRecuperacionSeGastan(t *testing.T) {
	h := newHarness()
	m := h.addMailbox(uuid.New(), "ana@acme.test", 0)
	ctx := context.Background()
	codes := activar(t, h, "ana@acme.test")

	typed := strings.ToLower(strings.ReplaceAll(codes[3], "-", " - "))
	v, err := h.uc.VerifyMFAByUsername(ctx, "ana@acme.test", typed)
	if err != nil || v.Method != domain.MFAMethodRecovery || v.RecoveryRemaining != domain.RecoveryCodeCount-1 {
		t.Fatalf("recuperacion: %+v %v", v, err)
	}
	if _, err := h.uc.VerifyMFAByUsername(ctx, "ana@acme.test", codes[3]); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("gastado: %v", err)
	}

	// Regenerar con un codigo que no vale no cambia nada; con uno que vale los sustituye todos.
	before := slices.Clone(h.mfa.rows[m.ID].RecoveryHashes)
	if _, err := h.uc.RegenerateRecoveryCodesByUsername(ctx, "ana@acme.test", "AAAAA-AAAAA"); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("regenerar sin codigo valido: %v", err)
	}
	if !slices.Equal(before, h.mfa.rows[m.ID].RecoveryHashes) {
		t.Fatal("un codigo invalido no regenera")
	}
	fresh, err := h.uc.RegenerateRecoveryCodesByUsername(ctx, "ana@acme.test", codes[0])
	if err != nil || len(fresh) != domain.RecoveryCodeCount || len(h.mfa.rows[m.ID].RecoveryHashes) != domain.RecoveryCodeCount {
		t.Fatalf("regenerar: %v %v", fresh, err)
	}
	if _, err := h.uc.VerifyMFAByUsername(ctx, "ana@acme.test", codes[1]); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("los codigos anteriores ya no valen: %v", err)
	}
	if _, err := h.uc.VerifyMFAByUsername(ctx, "ana@acme.test", fresh[9]); err != nil {
		t.Fatalf("los nuevos si: %v", err)
	}
}

// buzon relee el buzon del arnes: un rollback lo sustituye por su copia.
func buzon(h *harness, id uuid.UUID) *domain.Mailbox {
	for _, m := range h.mailboxes.items {
		if m.ID == id {
			return m
		}
	}
	return nil
}

func TestDesactivarYRestablecerLaVerificacion(t *testing.T) {
	h := newHarness()
	m := h.addMailbox(uuid.New(), "ana@acme.test", 0)
	ctx := context.Background()

	if err := h.uc.DisableMFAByUsername(ctx, "ana@acme.test", "111111"); !errors.Is(err, domain.ErrMFANotEnabled) {
		t.Fatalf("sin verificacion: %v", err)
	}
	codes := activar(t, h, "ana@acme.test")
	if err := h.uc.DisableMFAByUsername(ctx, "ana@acme.test", "BBBBB-BBBBB"); !errors.Is(err, domain.ErrInvalidMFACode) {
		t.Fatalf("codigo invalido: %v", err)
	}
	if !buzon(h, m.ID).MFAEnabled || h.mfa.rows[m.ID] == nil {
		t.Fatal("un codigo invalido no la apaga")
	}
	if err := h.uc.DisableMFAByUsername(ctx, "ana@acme.test", codes[0]); err != nil {
		t.Fatal(err)
	}
	if buzon(h, m.ID).MFAEnabled || h.mfa.rows[m.ID] != nil || !slices.Equal(h.events.mfaDisabledBy, []string{"user"}) || len(h.events.credentials) != 0 {
		t.Fatalf("apagada por el usuario: %+v %+v", m, h.events)
	}

	// El administrador la restablece: evento con su id y aviso de credencial que cierra las sesiones.
	actor := uuid.New()
	if err := h.uc.ResetMailboxMFA(ctx, m.TenantID, m.ID, actor); !errors.Is(err, domain.ErrMFANotEnabled) {
		t.Fatalf("restablecer sin verificacion: %v", err)
	}
	activar(t, h, "ana@acme.test")
	if err := h.uc.ResetMailboxMFA(ctx, uuid.New(), m.ID, actor); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("otra empresa: %v", err)
	}
	if err := h.uc.ResetMailboxMFA(ctx, m.TenantID, m.ID, actor); err != nil {
		t.Fatal(err)
	}
	if buzon(h, m.ID).MFAEnabled || h.mfa.rows[m.ID] != nil || !slices.Equal(h.events.mfaDisabledBy, []string{"user", "admin"}) ||
		!slices.Equal(h.events.mfaActors, []uuid.UUID{actor}) || len(h.events.outside) != 0 {
		t.Fatalf("restablecida: %+v %+v", m, h.events)
	}
	if len(h.events.credentials) != 1 || h.events.credentials[0].credential != domain.CredentialMFA ||
		!slices.Equal(h.events.credentials[0].changed, []domain.MailboxAttr{domain.AttrMFA}) {
		t.Fatalf("aviso de credencial: %+v", h.events.credentials)
	}

	// Si el aviso no se encola, el restablecimiento se deshace entero.
	activar(t, h, "ana@acme.test")
	h.events.fail = errors.New("outbox caida")
	if err := h.uc.ResetMailboxMFA(ctx, m.TenantID, m.ID, actor); err == nil {
		t.Fatal("sin outbox no se restablece")
	}
	h.events.fail = nil
	if !buzon(h, m.ID).MFAEnabled || h.mfa.rows[m.ID] == nil {
		t.Fatal("se deshizo")
	}
}

func TestBorrarUnBuzonBorraSuVerificacion(t *testing.T) {
	h := newHarness()
	m := h.addMailbox(uuid.New(), "ana@acme.test", 0)
	activar(t, h, "ana@acme.test")
	if err := h.uc.DeleteMailbox(context.Background(), m.TenantID, m.ID); err != nil {
		t.Fatal(err)
	}
	if len(h.mfa.rows) != 0 {
		t.Fatal("la verificacion del buzon borrado queda")
	}
}

// ── Reenvio externo ──────────────────────────────────────────────────────────

func forwardRule(name string, enabled bool, actions ...domain.FilterAction) domain.FilterRule {
	return domain.FilterRule{Name: name, Enabled: enabled,
		Conditions: []domain.FilterCondition{{Field: "from", Op: "contains", Value: "x"}}, Actions: actions}
}

func fwd(addr string) domain.FilterAction { return domain.FilterAction{Type: "forward", Address: addr} }

func TestReenvioExternoExigeReautenticarSoloLoNuevo(t *testing.T) {
	h := newHarness()
	tenant := uuid.New()
	h.addDomain(tenant, "acme.test", domain.DomainLimits{})
	h.aliasDomains.items = append(h.aliasDomains.items, &domain.AliasDomain{ID: uuid.New(), TenantID: tenant, AliasDomain: "acme-alias.test", TargetDomain: "acme.test"})
	h.addMailbox(tenant, "ana@acme.test", 0)
	ctx := context.Background()

	// Dominios propios y alias no son externos.
	internal := PutFiltersRequest{Forwarding: domain.Forwarding{Enabled: true, Addresses: []string{"luis@acme.test", "eva@acme-alias.test"}}}
	if _, err := h.uc.PutFiltersByUsername(ctx, "ana@acme.test", internal); err != nil {
		t.Fatal(err)
	}
	if len(h.events.forwarding) != 1 || len(h.events.forwarding[0].change.ExternalAdded) != 0 || !h.events.forwarding[0].change.ForwardingEnabled {
		t.Fatalf("encender el reenvio se anuncia sin externos: %+v", h.events.forwarding)
	}

	// Una regla apagada no reenvia: su destino externo no pide nada todavia.
	req := PutFiltersRequest{Rules: []domain.FilterRule{forwardRule("r", false, fwd("fuera@gmail.test"))}, Forwarding: internal.Forwarding}
	if _, err := h.uc.PutFiltersByUsername(ctx, "ana@acme.test", req); err != nil {
		t.Fatalf("regla apagada: %v", err)
	}
	if len(h.events.forwarding) != 1 {
		t.Fatalf("nada que anunciar: %+v", h.events.forwarding)
	}

	// Encenderla empieza a sacar correo: pide reautenticacion y no guarda nada sin ella.
	req.Rules = []domain.FilterRule{forwardRule("r", true, fwd("fuera@gmail.test"))}
	_, err := h.uc.PutFiltersByUsername(ctx, "ana@acme.test", req)
	var fe *domain.ForwardingError
	if !errors.As(err, &fe) || !errors.Is(err, domain.ErrReauthRequired) || !slices.Equal(fe.Addresses, []string{"fuera@gmail.test"}) {
		t.Fatalf("sin reautenticar: %v", err)
	}
	if got, _ := h.uc.FiltersByUsername(ctx, "ana@acme.test"); got.Rules[0].Enabled {
		t.Fatal("no se guardo")
	}
	req.Reauthenticated = true
	if _, err := h.uc.PutFiltersByUsername(ctx, "ana@acme.test", req); err != nil {
		t.Fatal(err)
	}
	last := h.events.forwarding[len(h.events.forwarding)-1].change
	if !slices.Equal(last.ExternalAdded, []string{"fuera@gmail.test"}) || len(last.ExternalRemoved) != 0 {
		t.Fatalf("anuncio: %+v", last)
	}

	// Lo ya activo no vuelve a pedir nada; quitarlo se anuncia como retirado.
	req.Reauthenticated = false
	if _, err := h.uc.PutFiltersByUsername(ctx, "ana@acme.test", req); err != nil {
		t.Fatalf("sin cambios: %v", err)
	}
	if _, err := h.uc.PutFiltersByUsername(ctx, "ana@acme.test", internal); err != nil {
		t.Fatal(err)
	}
	last = h.events.forwarding[len(h.events.forwarding)-1].change
	if !slices.Equal(last.ExternalRemoved, []string{"fuera@gmail.test"}) || len(h.events.outside) != 0 {
		t.Fatalf("retirada: %+v", last)
	}
	if !slices.Equal(h.policies.locks, []bool{false, false, false, false, false, false}) {
		t.Fatalf("cada guardado toma el cerrojo compartido de la politica: %v", h.policies.locks)
	}
}

func TestPoliticaSinReenvioExternoRetiraLoGuardado(t *testing.T) {
	h := newHarness()
	tenant, otra := uuid.New(), uuid.New()
	h.addDomain(tenant, "acme.test", domain.DomainLimits{})
	h.addMailbox(tenant, "ana@acme.test", 0)
	h.addMailbox(tenant, "luis@acme.test", 0)
	h.addMailbox(tenant, "eva@acme.test", 0)
	h.addMailbox(otra, "ext@otra.test", 0)
	ctx := context.Background()
	put := func(user string, req PutFiltersRequest) {
		t.Helper()
		req.Reauthenticated = true
		if _, err := h.uc.PutFiltersByUsername(ctx, user, req); err != nil {
			t.Fatal(err)
		}
	}
	put("ana@acme.test", PutFiltersRequest{
		Rules: []domain.FilterRule{
			forwardRule("solo fuera", true, fwd("fuera@gmail.test")),
			forwardRule("mixta", true, domain.FilterAction{Type: "flag"}, fwd("fuera@gmail.test"), fwd("luis@acme.test")),
		},
		Forwarding: domain.Forwarding{Enabled: true, Addresses: []string{"copia@yahoo.test"}},
	})
	put("luis@acme.test", PutFiltersRequest{Forwarding: domain.Forwarding{Enabled: false, Addresses: []string{"ana@acme.test", "viejo@gmail.test"}}})
	put("eva@acme.test", PutFiltersRequest{Forwarding: domain.Forwarding{Enabled: true, Addresses: []string{"ana@acme.test"}}})
	put("ext@otra.test", PutFiltersRequest{Forwarding: domain.Forwarding{Enabled: true, Addresses: []string{"fuera@gmail.test"}}})

	by := uuid.New()
	p, removed, err := h.uc.SetMailPolicy(ctx, tenant, by, false)
	if err != nil || p.ExternalForwardingAllowed || removed != 2 || p.UpdatedBy == nil || *p.UpdatedBy != by {
		t.Fatalf("apagar: %+v %d %v", p, removed, err)
	}
	ana, _ := h.uc.FiltersByUsername(ctx, "ana@acme.test")
	if len(ana.Rules) != 1 || ana.Rules[0].Name != "mixta" || len(ana.Rules[0].Actions) != 2 ||
		ana.Forwarding.Enabled || len(ana.Forwarding.Addresses) != 0 {
		t.Fatalf("ana: %+v", ana)
	}
	if strings.Contains(ana.ScriptData, "gmail") || strings.Contains(ana.ScriptData, "yahoo") || !strings.Contains(ana.ScriptData, `redirect "luis@acme.test"`) {
		t.Fatalf("el script se regenera sin los externos: %s", ana.ScriptData)
	}
	luis, _ := h.uc.FiltersByUsername(ctx, "luis@acme.test")
	if !slices.Equal(luis.Forwarding.Addresses, []string{"ana@acme.test"}) {
		t.Fatalf("tambien las direcciones de un reenvio apagado: %+v", luis.Forwarding)
	}
	if ext, _ := h.uc.FiltersByUsername(ctx, "ext@otra.test"); len(ext.Forwarding.Addresses) != 1 {
		t.Fatal("otra empresa no se toca")
	}
	if !slices.Equal(h.events.policyRemoved, []int{2}) || h.policies.locks[len(h.policies.locks)-1] != true {
		t.Fatalf("evento y cerrojo: %v %v", h.events.policyRemoved, h.policies.locks)
	}

	// Con la politica apagada no se guarda ningun destino externo, ni reautenticado ni apagado.
	_, err = h.uc.PutFiltersByUsername(ctx, "eva@acme.test", PutFiltersRequest{
		Forwarding: domain.Forwarding{Enabled: false, Addresses: []string{"nuevo@gmail.test"}}, Reauthenticated: true,
	})
	var fe *domain.ForwardingError
	if !errors.As(err, &fe) || !errors.Is(err, domain.ErrExternalForwardingDisabled) || !slices.Equal(fe.Addresses, []string{"nuevo@gmail.test"}) {
		t.Fatalf("prohibido: %v", err)
	}

	// Volver a permitirlo no retira nada.
	got, _ := h.uc.MailPolicy(ctx, tenant)
	if got.ExternalForwardingAllowed {
		t.Fatal("se lee apagada")
	}
	if _, removed, err := h.uc.SetMailPolicy(ctx, tenant, by, true); err != nil || removed != 0 {
		t.Fatalf("encender: %d %v", removed, err)
	}
	if def, _ := h.uc.MailPolicy(ctx, otra); !def.ExternalForwardingAllowed {
		t.Fatal("sin fila se permite")
	}
}
