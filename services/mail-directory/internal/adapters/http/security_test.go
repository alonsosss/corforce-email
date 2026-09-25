package http

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/totp"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/adapters/secrets"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
)

// ── Falsos de la seguridad del buzon ─────────────────────────────────────────

type secPolicies struct {
	rows  map[uuid.UUID]*domain.MailPolicy
	locks []bool
}

func (f *secPolicies) Lock(_ context.Context, _ uuid.UUID, exclusive bool) error {
	f.locks = append(f.locks, exclusive)
	return nil
}
func (f *secPolicies) Get(_ context.Context, tenantID uuid.UUID) (*domain.MailPolicy, error) {
	p, ok := f.rows[tenantID]
	if !ok {
		return nil, domain.ErrNotFound
	}
	c := *p
	return &c, nil
}
func (f *secPolicies) Upsert(_ context.Context, p *domain.MailPolicy) error {
	p.UpdatedAt = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	c := *p
	f.rows[p.TenantID] = &c
	return nil
}

type secMFA struct {
	rows map[uuid.UUID]*domain.MailboxMFA
}

func (f *secMFA) Get(_ context.Context, _, mailboxID uuid.UUID) (*domain.MailboxMFA, error) {
	m, ok := f.rows[mailboxID]
	if !ok {
		return nil, domain.ErrNotFound
	}
	c := *m
	c.RecoveryHashes = slices.Clone(m.RecoveryHashes)
	return &c, nil
}
func (f *secMFA) Create(_ context.Context, m *domain.MailboxMFA) error {
	if _, ok := f.rows[m.MailboxID]; ok {
		return domain.ErrAlreadyExists
	}
	c := *m
	f.rows[m.MailboxID] = &c
	return nil
}
func (f *secMFA) AdvanceStep(_ context.Context, _, mailboxID uuid.UUID, step int64) (bool, error) {
	m, ok := f.rows[mailboxID]
	if !ok || m.LastStep >= step {
		return false, nil
	}
	m.LastStep = step
	return true, nil
}
func (f *secMFA) ConsumeRecoveryCode(_ context.Context, _, mailboxID uuid.UUID, hash string) (int, bool, error) {
	m, ok := f.rows[mailboxID]
	if !ok {
		return 0, false, nil
	}
	i := slices.Index(m.RecoveryHashes, hash)
	if i < 0 {
		return 0, false, nil
	}
	m.RecoveryHashes = slices.Delete(m.RecoveryHashes, i, i+1)
	return len(m.RecoveryHashes), true, nil
}
func (f *secMFA) ReplaceRecoveryCodes(_ context.Context, _, mailboxID uuid.UUID, hashes []string) error {
	f.rows[mailboxID].RecoveryHashes = slices.Clone(hashes)
	return nil
}
func (f *secMFA) Delete(_ context.Context, _, mailboxID uuid.UUID) (bool, error) {
	_, ok := f.rows[mailboxID]
	delete(f.rows, mailboxID)
	return ok, nil
}

// secDomains responde que dominios son de la empresa.
type secDomains struct {
	ports.DomainRepository
	owned map[string]bool
}

func (f secDomains) OwnedNames(_ context.Context, _ uuid.UUID, names []string) ([]string, error) {
	var out []string
	for _, n := range names {
		if f.owned[n] {
			out = append(out, n)
		}
	}
	return out, nil
}

// secSealer "cifra" poniendo delante los datos autenticados: basta para comprobar que el secreto se
// abre solo con el id de su buzon.
type secSealer struct{}

func (secSealer) Seal(plain, aad []byte) ([]byte, error) {
	return append(append(append([]byte{}, aad...), '|'), plain...), nil
}
func (secSealer) Open(sealed, aad []byte) ([]byte, error) {
	prefix := append(append([]byte{}, aad...), '|')
	if !bytes.HasPrefix(sealed, prefix) {
		return nil, domain.ErrNotFound
	}
	return sealed[len(prefix):], nil
}

// secTOTP es el TOTP real a la hora del entorno de la prueba.
type secTOTP struct{ now func() time.Time }

func (f secTOTP) ValidateStep(secret, code string, _ time.Time) (int64, bool) {
	return secrets.TOTP{}.ValidateStep(secret, code, f.now())
}

type secAppPasswords struct {
	ports.AppPasswordRepository
	items []domain.AppPassword
}

func (f *secAppPasswords) List(_ context.Context, _, mailboxID uuid.UUID) ([]domain.AppPassword, error) {
	var out []domain.AppPassword
	for _, p := range f.items {
		if p.MailboxID == mailboxID {
			out = append(out, p)
		}
	}
	return out, nil
}
func (f *secAppPasswords) Create(_ context.Context, p *domain.AppPassword) error {
	f.items = append(f.items, *p)
	return nil
}
func (f *secAppPasswords) Get(_ context.Context, _, mailboxID, id uuid.UUID) (*domain.AppPassword, error) {
	for _, p := range f.items {
		if p.MailboxID == mailboxID && p.ID == id {
			c := p
			return &c, nil
		}
	}
	return nil, domain.ErrNotFound
}
func (f *secAppPasswords) Delete(_ context.Context, _, mailboxID, id uuid.UUID) error {
	f.items = slices.DeleteFunc(f.items, func(p domain.AppPassword) bool { return p.MailboxID == mailboxID && p.ID == id })
	return nil
}

func (f *settingsMailboxes) SetMFAEnabled(_ context.Context, _, _ uuid.UUID, enabled bool) error {
	f.m.MFAEnabled = enabled
	return nil
}

func (settingsSecrets) GenerateAppPassword() (string, error) {
	return "generada-en-el-servidor-32-chars", nil
}

var recoverySeq int

func (settingsSecrets) GenerateRecoveryCode() (string, error) {
	recoverySeq++
	code := []byte("AAAAAAAAAA")
	for i, n := len(code)-1, recoverySeq; n > 0 && i >= 0; i, n = i-1, n/len(domain.RecoveryCodeAlphabet) {
		code[i] = domain.RecoveryCodeAlphabet[n%len(domain.RecoveryCodeAlphabet)]
	}
	return string(code), nil
}

// admin es una llamada del panel con el rol de administrador de la empresa del buzon.
func (e *settingsEnv) admin(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := middleware.WithIdentity(req.Context(), uuid.NewString(), e.m.TenantID.String())
	ctx = context.WithValue(ctx, middleware.CtxRoles, []string{middleware.RoleTenantAdmin})
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

func expectCode(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) envelope {
	t.Helper()
	env := decodeEnvelope(t, rec, nil)
	if rec.Code != status || env.Error == nil || env.Error.Code != code {
		t.Fatalf("se esperaba %d %s y salio %d %s", status, code, rec.Code, rec.Body)
	}
	return env
}

// ── Verificacion en dos pasos ────────────────────────────────────────────────

const mfaPath = "/internal/mail-directory/mfa"

func TestVerificacionEnDosPasosDelBuzon(t *testing.T) {
	e := settingsServer(t)
	user := "?username=ana@acme.test"
	var status mfaStatusResponse
	decodeEnvelope(t, e.do(http.MethodGet, mfaPath+user, ""), &status)
	if status.Enabled || status.EnabledAt != nil || status.RecoveryRemaining != 0 {
		t.Fatalf("sin activar: %+v", status)
	}

	secret, _ := totp.GenerateSecret()
	code, _ := totp.Generate(secret, e.now)
	expectCode(t, e.do(http.MethodPost, mfaPath+"/activate"+user, `{"secret":"`+secret+`","code":"000000"}`), http.StatusUnprocessableEntity, "INVALID_MFA_CODE")
	expectField(t, e.do(http.MethodPost, mfaPath+"/activate"+user, `{"secret":"no-base32","code":"`+code+`"}`), "secret")
	if e.mailboxes.m.MFAEnabled || len(e.mfa.rows) != 0 {
		t.Fatal("un codigo o secreto invalidos no activan nada")
	}

	rec := e.do(http.MethodPost, mfaPath+"/activate"+user, `{"secret":"`+strings.ToLower(secret)+`","code":" `+code[:3]+" "+code[3:]+`"}`)
	var codes recoveryCodesResponse
	decodeEnvelope(t, rec, &codes)
	if rec.Code != http.StatusCreated || len(codes.RecoveryCodes) != domain.RecoveryCodeCount {
		t.Fatalf("activar: %d %s", rec.Code, rec.Body)
	}
	shape := regexp.MustCompile(`^[A-HJ-NP-Z2-9]{5}-[A-HJ-NP-Z2-9]{5}$`)
	for _, c := range codes.RecoveryCodes {
		if !shape.MatchString(c) {
			t.Fatalf("codigo de recuperacion %q", c)
		}
	}
	row := e.mfa.rows[e.m.ID]
	if !e.mailboxes.m.MFAEnabled || e.events.mfaEnabled != 1 || row == nil || bytes.Contains(row.SecretEnc, []byte(secret)) == false ||
		!bytes.HasPrefix(row.SecretEnc, e.m.ID[:]) || slices.Contains(row.RecoveryHashes, codes.RecoveryCodes[0]) {
		t.Fatalf("estado tras activar: %+v", row)
	}
	expectCode(t, e.do(http.MethodPost, mfaPath+"/activate"+user, `{"secret":"`+secret+`","code":"`+code+`"}`), http.StatusConflict, "MFA_ALREADY_ENABLED")

	// El codigo de la activacion ya no vale como segundo paso: su paso quedo como el ultimo usado.
	expectCode(t, e.do(http.MethodPost, mfaPath+"/verify"+user, `{"code":"`+code+`"}`), http.StatusUnprocessableEntity, "INVALID_MFA_CODE")
	e.now = e.now.Add(30 * time.Second)
	next, _ := totp.Generate(secret, e.now)
	var verified mfaVerifyResponse
	rec = e.do(http.MethodPost, mfaPath+"/verify"+user, `{"code":"`+next+`"}`)
	decodeEnvelope(t, rec, &verified)
	if rec.Code != http.StatusOK || verified.Method != "totp" || verified.RecoveryRemaining != domain.RecoveryCodeCount {
		t.Fatalf("verificar TOTP: %d %s", rec.Code, rec.Body)
	}
	expectCode(t, e.do(http.MethodPost, mfaPath+"/verify"+user, `{"code":"`+next+`"}`), http.StatusUnprocessableEntity, "INVALID_MFA_CODE")

	// Un codigo de recuperacion vale tecleado en minusculas, con espacios o sin guion, y una sola vez.
	typed := strings.ToLower(strings.ReplaceAll(codes.RecoveryCodes[0], "-", " "))
	rec = e.do(http.MethodPost, mfaPath+"/verify"+user, `{"code":"`+typed+`"}`)
	decodeEnvelope(t, rec, &verified)
	if rec.Code != http.StatusOK || verified.Method != "recovery" || verified.RecoveryRemaining != domain.RecoveryCodeCount-1 {
		t.Fatalf("verificar recuperacion: %d %s", rec.Code, rec.Body)
	}
	expectCode(t, e.do(http.MethodPost, mfaPath+"/verify"+user, `{"code":"`+codes.RecoveryCodes[0]+`"}`), http.StatusUnprocessableEntity, "INVALID_MFA_CODE")
	decodeEnvelope(t, e.do(http.MethodGet, mfaPath+user, ""), &status)
	if !status.Enabled || status.EnabledAt == nil || status.RecoveryRemaining != domain.RecoveryCodeCount-1 {
		t.Fatalf("estado: %+v", status)
	}

	// Regenerar exige un codigo y sustituye todos los de recuperacion.
	expectCode(t, e.do(http.MethodPost, mfaPath+"/recovery-codes"+user, `{"code":""}`), http.StatusUnprocessableEntity, "INVALID_MFA_CODE")
	var fresh recoveryCodesResponse
	rec = e.do(http.MethodPost, mfaPath+"/recovery-codes"+user, `{"code":"`+codes.RecoveryCodes[1]+`"}`)
	decodeEnvelope(t, rec, &fresh)
	if rec.Code != http.StatusOK || len(fresh.RecoveryCodes) != domain.RecoveryCodeCount || slices.Contains(fresh.RecoveryCodes, codes.RecoveryCodes[2]) {
		t.Fatalf("regenerar: %d %s", rec.Code, rec.Body)
	}
	expectCode(t, e.do(http.MethodPost, mfaPath+"/verify"+user, `{"code":"`+codes.RecoveryCodes[2]+`"}`), http.StatusUnprocessableEntity, "INVALID_MFA_CODE")

	// Desactivar exige un codigo; despues ya no hay nada que verificar.
	expectCode(t, e.do(http.MethodDelete, mfaPath+user, `{"code":"123456"}`), http.StatusUnprocessableEntity, "INVALID_MFA_CODE")
	if rec := e.do(http.MethodDelete, mfaPath+user, `{"code":"`+fresh.RecoveryCodes[0]+`"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("desactivar: %d %s", rec.Code, rec.Body)
	}
	if e.mailboxes.m.MFAEnabled || len(e.mfa.rows) != 0 || !slices.Equal(e.events.mfaDisabled, []string{"user"}) || e.events.credentials != 0 {
		t.Fatalf("tras desactivar: %+v %v", e.mailboxes.m, e.events.mfaDisabled)
	}
	expectCode(t, e.do(http.MethodPost, mfaPath+"/verify"+user, `{"code":"`+next+`"}`), http.StatusConflict, "MFA_NOT_ENABLED")
}

func TestRestablecerLaVerificacionDesdeElPanel(t *testing.T) {
	e := settingsServer(t)
	path := "/api/v1/mailboxes/" + e.m.ID.String() + "/mfa"
	expectCode(t, e.admin(http.MethodDelete, path, ""), http.StatusConflict, "MFA_NOT_ENABLED")

	e.mfa.rows[e.m.ID] = &domain.MailboxMFA{MailboxID: e.m.ID, TenantID: e.m.TenantID}
	e.m.MFAEnabled = true
	if rec := e.admin(http.MethodDelete, path, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("restablecer: %d %s", rec.Code, rec.Body)
	}
	if e.m.MFAEnabled || len(e.mfa.rows) != 0 || !slices.Equal(e.events.mfaDisabled, []string{"admin"}) ||
		e.events.credentials != 1 || e.events.credential != domain.CredentialMFA {
		t.Fatalf("tras restablecer: %+v %+v", e.m, e.events)
	}
	if rec := e.admin(http.MethodDelete, "/api/v1/mailboxes/"+uuid.NewString()+"/mfa", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("buzon ajeno: %d", rec.Code)
	}
	// Sin rol de empresa el permiso se pregunta a access-control, que aqui no responde: falla cerrado.
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	req = req.WithContext(middleware.WithIdentity(req.Context(), uuid.NewString(), e.m.TenantID.String()))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code < 400 {
		t.Fatalf("sin permiso: %d", rec.Code)
	}
}

func TestElDetalleDelBuzonDiceSiTieneVerificacion(t *testing.T) {
	e := settingsServer(t)
	e.m.MFAEnabled = true
	rec := e.admin(http.MethodGet, "/api/v1/mailboxes/"+e.m.ID.String(), "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"mfa_enabled":true`) {
		t.Fatalf("detalle: %d %s", rec.Code, rec.Body)
	}
}

// ── Contrasenas de aplicacion del webmail ────────────────────────────────────

func TestContrasenasDeAplicacionDelWebmail(t *testing.T) {
	e := settingsServer(t)
	base := "/internal/mail-directory/app-passwords"
	user := "?username=ana@acme.test"
	expectCode(t, e.do(http.MethodPost, base+user, `{"name":""}`), http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	rec := e.do(http.MethodPost, base+user, `{"name":"Portátil","imap":true,"pop3":false,"smtp":true,"sieve":false,"dav":false}`)
	var created struct {
		ID         uuid.UUID `json:"id"`
		Name       string    `json:"name"`
		IMAPAccess bool      `json:"imap_access"`
		POP3Access bool      `json:"pop3_access"`
		Password   string    `json:"password"`
	}
	decodeEnvelope(t, rec, &created)
	if rec.Code != http.StatusCreated || created.ID == uuid.Nil || created.Name != "Portátil" || !created.IMAPAccess || created.POP3Access ||
		created.Password != "generada-en-el-servidor-32-chars" || strings.Contains(rec.Body.String(), "password_hash") {
		t.Fatalf("crear: %d %s", rec.Code, rec.Body)
	}
	rec = e.do(http.MethodGet, base+user, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), created.ID.String()) || strings.Contains(rec.Body.String(), `"password"`) {
		t.Fatalf("listar: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"max":25`) {
		t.Fatalf("la lista lleva el tope: %s", rec.Body)
	}
	if rec := e.do(http.MethodDelete, base+"/"+created.ID.String()+user, ""); rec.Code != http.StatusNoContent || len(e.apps.items) != 0 {
		t.Fatalf("borrar: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(http.MethodDelete, base+"/"+created.ID.String()+user, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("borrar otra vez: %d", rec.Code)
	}
	if rec := e.do(http.MethodGet, base+"?username=nadie@acme.test", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("buzon desconocido: %d", rec.Code)
	}
}

func TestRutasDeSeguridadCerradasALasPersonas(t *testing.T) {
	e := settingsServer(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, mfaPath + "?username=ana@acme.test"},
		{http.MethodPost, mfaPath + "/activate?username=ana@acme.test"},
		{http.MethodPost, mfaPath + "/verify?username=ana@acme.test"},
		{http.MethodPost, mfaPath + "/recovery-codes?username=ana@acme.test"},
		{http.MethodDelete, mfaPath + "?username=ana@acme.test"},
		{http.MethodGet, "/internal/mail-directory/app-passwords?username=ana@acme.test"},
		{http.MethodPost, "/internal/mail-directory/app-passwords?username=ana@acme.test"},
		{http.MethodDelete, "/internal/mail-directory/app-passwords/" + uuid.NewString() + "?username=ana@acme.test"},
	} {
		if rec := e.do(c.method, c.path, `{"code":"123456","name":"x"}`, uuid.NewString()); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s con usuario: %d", c.method, c.path, rec.Code)
		}
	}
}

// ── Reenvio externo y politica de la empresa ─────────────────────────────────

func TestReenvioExternoYPoliticaDeLaEmpresa(t *testing.T) {
	e := settingsServer(t)
	filters := "/internal/mail-directory/filters?username=ana@acme.test"
	policy := "/api/v1/mail-directory/mail-policy"

	var got mailPolicyResponse
	decodeEnvelope(t, e.admin(http.MethodGet, policy, ""), &got)
	if !got.ExternalForwardingAllowed || got.UpdatedAt != nil {
		t.Fatalf("por defecto se permite: %+v", got)
	}

	// Un destino de la propia empresa no pide reautenticacion; uno externo nuevo si.
	if rec := e.do(http.MethodPut, filters, `{"forwarding":{"enabled":true,"addresses":["luis@acme.test"]}}`); rec.Code != http.StatusOK {
		t.Fatalf("reenvio interno: %d %s", rec.Code, rec.Body)
	}
	if len(e.events.forwarding) != 1 || len(e.events.forwarding[0].ExternalAdded) != 0 || !e.events.forwarding[0].ForwardingEnabled {
		t.Fatalf("encender el reenvio se anuncia: %+v", e.events.forwarding)
	}
	body := `{"forwarding":{"enabled":true,"addresses":["luis@acme.test","Fuera@Gmail.test","otro@yahoo.test"]}`
	env := expectCode(t, e.do(http.MethodPut, filters, body+`}`), http.StatusForbidden, "REAUTH_REQUIRED")
	if env.Error.Details["addresses"] != "fuera@gmail.test,otro@yahoo.test" {
		t.Fatalf("destinos: %v", env.Error.Details)
	}
	if rec := e.do(http.MethodPut, filters, body+`,"reauthenticated":true}`); rec.Code != http.StatusOK {
		t.Fatalf("reautenticado: %d %s", rec.Code, rec.Body)
	}
	if last := e.events.forwarding[len(e.events.forwarding)-1]; !slices.Equal(last.ExternalAdded, []string{"fuera@gmail.test", "otro@yahoo.test"}) {
		t.Fatalf("anuncio: %+v", last)
	}
	// Volver a guardar lo mismo no pide nada ni anuncia nada.
	before := len(e.events.forwarding)
	if rec := e.do(http.MethodPut, filters, body+`}`); rec.Code != http.StatusOK || len(e.events.forwarding) != before {
		t.Fatalf("sin cambios: %d %s", rec.Code, rec.Body)
	}

	expectField(t, e.admin(http.MethodPut, policy, `{}`), "external_forwarding_allowed")
	rec := e.admin(http.MethodPut, policy, `{"external_forwarding_allowed":false}`)
	var updated mailPolicyUpdateResponse
	decodeEnvelope(t, rec, &updated)
	if rec.Code != http.StatusOK || updated.ExternalForwardingAllowed || updated.RemovedMailboxes != 1 || updated.UpdatedAt == nil {
		t.Fatalf("apagar: %d %s", rec.Code, rec.Body)
	}
	saved := e.filters.saved
	if !slices.Equal(saved.Forwarding.Addresses, []string{"luis@acme.test"}) || !saved.Forwarding.Enabled ||
		strings.Contains(saved.ScriptData, "gmail") || !slices.Equal(e.events.policies, []int{1}) {
		t.Fatalf("filtros tras apagar: %+v %v", saved, e.events.policies)
	}
	if !slices.Equal(e.policies.locks, []bool{false, false, false, false, true}) {
		t.Fatalf("cerrojos: %v", e.policies.locks)
	}

	env = expectCode(t, e.do(http.MethodPut, filters, `{"forwarding":{"enabled":false,"addresses":["x@gmail.test"]},"reauthenticated":true}`),
		http.StatusUnprocessableEntity, "EXTERNAL_FORWARDING_DISABLED")
	if env.Error.Details["addresses"] != "x@gmail.test" {
		t.Fatalf("destinos: %v", env.Error.Details)
	}
}
