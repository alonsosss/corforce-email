package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/app"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/alonsosss/corforce-email/services/webmail/internal/ports"
	"go.uber.org/zap"
)

const (
	allowedOrigin = "https://app.example.com"
	testUser      = "ana@empresa.pe"
	testPass      = "correcta-larga"
	testCell      = "pe-01"
)

type stubAuth struct{}

const (
	testTenant  = "11111111-1111-4111-8111-111111111111"
	testMailbox = "22222222-2222-4222-8222-222222222222"
	// legacyUser inicia sesion como un buzon ante un mail-auth que aun no devuelve empresa ni buzon.
	legacyUser = "antigua@empresa.pe"
)

func (stubAuth) Verify(_ context.Context, username, password, _ string) (domain.Identity, error) {
	switch {
	case username == testUser && password == testPass:
		return domain.Identity{Username: username, DisplayName: "Ana", TenantID: testTenant, MailboxID: testMailbox}, nil
	case username == legacyUser && password == testPass:
		return domain.Identity{Username: username, DisplayName: "Antigua"}, nil
	}
	return domain.Identity{}, domain.ErrInvalidCredentials
}

type memStore struct {
	mu sync.Mutex
	m  map[string]domain.Session
}

func (s *memStore) Create(_ context.Context, key string, sess domain.Session, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = sess
	return nil
}
func (s *memStore) Get(_ context.Context, key string) (domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[key]
	if !ok {
		return domain.Session{}, domain.ErrSessionInvalid
	}
	return sess, nil
}
func (s *memStore) Touch(context.Context, string, time.Duration) error { return nil }
func (s *memStore) Delete(_ context.Context, key, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}
func (s *memStore) Revoke(context.Context, string, time.Time) error { return nil }
func (s *memStore) RevokedAt(context.Context, string) (time.Time, error) {
	return time.Time{}, nil
}

type stubMail struct{ mb *stubMailbox }

func (m stubMail) Open(context.Context, string) (ports.Mailbox, error) { return m.mb, nil }

type stubMailbox struct {
	mu        sync.Mutex
	listed    string
	listQuery domain.ListQuery
	part      domain.Part
	body      string
	expunged  []uint32
	partReqs  []string
	moved     []uint32
	created   []string
	renamed   []string
	deleted   []string
	emptied   []string
	folders   []domain.Folder
	raw       string
	capped    bool
}

func (m *stubMailbox) Close() error { return nil }
func (m *stubMailbox) Folders(context.Context, bool) ([]domain.Folder, error) {
	if m.folders != nil {
		return m.folders, nil
	}
	return []domain.Folder{
		{Name: "INBOX", Delimiter: "/", Role: domain.RoleInbox, Selectable: true},
		{Name: "Sent", Delimiter: "/", Role: domain.RoleSent, Selectable: true},
		{Name: "Drafts", Delimiter: "/", Role: domain.RoleDrafts, Selectable: true},
		{Name: "Trash", Delimiter: "/", Role: domain.RoleTrash, Selectable: true},
		{Name: "Junk", Delimiter: "/", Role: domain.RoleJunk, Selectable: true},
		{Name: "Clientes", Delimiter: "/", Selectable: true},
		{Name: "Clientes/2026", Delimiter: "/", Selectable: true},
		{Name: "Viejos", Delimiter: "/", Selectable: true},
	}, nil
}
func (m *stubMailbox) Quota(context.Context) (*domain.Quota, error) { return nil, nil }
func (m *stubMailbox) List(_ context.Context, folder string, q domain.ListQuery) (domain.MessagePage, error) {
	m.listed, m.listQuery = folder, q
	return domain.MessagePage{Total: 2000, Capped: m.capped}, nil
}
func (m *stubMailbox) Read(context.Context, string, uint32, domain.ReadOptions) (*domain.RawMessage, error) {
	return nil, domain.ErrMessageNotFound
}
func (m *stubMailbox) OpenPart(_ context.Context, _ string, _ uint32, partID string, _ int64) (domain.Part, io.ReadCloser, error) {
	m.partReqs = append(m.partReqs, partID)
	return m.part, io.NopCloser(strings.NewReader(m.body)), nil
}
func (m *stubMailbox) ReplyReference(context.Context, string, uint32) (domain.ReplyReference, error) {
	return domain.ReplyReference{}, nil
}
func (m *stubMailbox) SetFlags(_ context.Context, _ string, uids []uint32, _ domain.FlagChange) (int, error) {
	return len(uids), nil
}
func (m *stubMailbox) Move(_ context.Context, _ string, uids []uint32, _ string) (int, error) {
	m.moved = append(m.moved, uids...)
	return len(uids), nil
}
func (m *stubMailbox) Expunge(_ context.Context, _ string, uids []uint32) (int, error) {
	m.expunged = append(m.expunged, uids...)
	return len(uids), nil
}
func (m *stubMailbox) Empty(_ context.Context, folder string) (int, error) {
	m.emptied = append(m.emptied, folder)
	return 3, nil
}
func (m *stubMailbox) Append(context.Context, string, []byte, []domain.Flag, time.Time) (domain.AppendedMessage, error) {
	return domain.AppendedMessage{UID: 1, UIDValidity: 1}, nil
}
func (m *stubMailbox) Stat(_ context.Context, _ string, uid uint32) (domain.StoredMessage, error) {
	if m.raw == "" {
		return domain.StoredMessage{}, domain.ErrMessageNotFound
	}
	return domain.StoredMessage{UIDValidity: 1, UID: uid, Size: int64(len(m.raw))}, nil
}
func (m *stubMailbox) OpenRaw(ctx context.Context, folder string, uid uint32, maxBytes int64) (domain.StoredMessage, io.ReadCloser, error) {
	st, err := m.Stat(ctx, folder, uid)
	if err != nil {
		return domain.StoredMessage{}, nil, err
	}
	if st.Size > maxBytes {
		return domain.StoredMessage{}, nil, domain.ErrMessageTooLarge
	}
	return st, io.NopCloser(strings.NewReader(m.raw)), nil
}
func (m *stubMailbox) FindByMessageID(context.Context, string, string) (uint32, error) { return 0, nil }
func (m *stubMailbox) CreateFolder(_ context.Context, name string) error {
	m.created = append(m.created, name)
	return nil
}
func (m *stubMailbox) RenameFolder(_ context.Context, name, newName string) error {
	m.renamed = append(m.renamed, name+"->"+newName)
	return nil
}
func (m *stubMailbox) DeleteFolder(_ context.Context, name string) error {
	m.deleted = append(m.deleted, name)
	return nil
}

type nopSender struct{}

func (nopSender) Send(context.Context, string, string, []string, []byte) error { return nil }

type nopComposer struct{}

func (nopComposer) Compose(domain.Outgoing, bool) ([]byte, error) { return []byte("x"), nil }
func (nopComposer) Finalize(stored []byte, _ time.Time) (domain.FinalizedMessage, error) {
	return domain.FinalizedMessage{Wire: stored, Stored: stored}, nil
}

type nopSanitizer struct{}

func (nopSanitizer) Incoming(html string, _ domain.SanitizeOptions) domain.SanitizedHTML {
	return domain.SanitizedHTML{HTML: html}
}
func (nopSanitizer) Outgoing(html string) (string, string) { return html, html }

// stubDirectory hace de mail-directory: el buzon de prueba puede enviar tambien como ventas.
type stubDirectory struct{}

func (stubDirectory) SenderIdentities(context.Context, string) ([]string, error) {
	return []string{"ventas@empresa.pe"}, nil
}

// stubVacations hace de mail-directory para la respuesta automatica: anota con que buzon y con que
// datos se le llamo y devuelve lo que la prueba prepare.
type stubVacations struct {
	mu       sync.Mutex
	username string
	input    *domain.VacationInput
	current  domain.Vacation
	err      error
}

func (s *stubVacations) Vacation(_ context.Context, username string) (domain.Vacation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.username = username
	return s.current, s.err
}

// stubAddressBook hace de mail-directory para la libreta: anota con que buzon, texto y tope se le llamo.
type stubAddressBook struct {
	mu       sync.Mutex
	username string
	query    string
	limit    int
	entries  []domain.AddressBookEntry
	err      error
}

func (s *stubAddressBook) Search(_ context.Context, username, query string, limit int) ([]domain.AddressBookEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.username, s.query, s.limit = username, query, limit
	return s.entries, s.err
}

func (s *stubVacations) SetVacation(_ context.Context, username string, in domain.VacationInput) (domain.Vacation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.username, s.input = username, &in
	if s.err != nil {
		return domain.Vacation{}, s.err
	}
	s.current = domain.Vacation{Enabled: in.Enabled, Subject: in.Subject, Message: in.Message, IntervalDays: in.IntervalDays, StartsOn: in.StartsOn, EndsOn: in.EndsOn}
	return s.current, nil
}

// memLedger es el registro de envios en memoria, con la misma semantica que el de Redis.
type memLedger struct {
	mu  sync.Mutex
	m   map[string]domain.SendRecord
	seq int
}

func (l *memLedger) Reserve(_ context.Context, key string, rec domain.SendRecord, _ time.Duration) (domain.SendRecord, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if current, ok := l.m[key]; ok {
		return current, false, nil
	}
	l.seq++
	rec.Token = fmt.Sprintf("marca-%d", l.seq)
	l.m[key] = rec
	return rec, true, nil
}

func (l *memLedger) Update(_ context.Context, key string, rec domain.SendRecord, _ time.Duration) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if current, ok := l.m[key]; !ok || current.Token != rec.Token {
		return false, nil
	}
	l.m[key] = rec
	return true, nil
}

func (l *memLedger) Release(_ context.Context, key, token string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if current, ok := l.m[key]; ok && current.Token == token {
		delete(l.m, key)
	}
	return nil
}

func newTestHandler(t *testing.T) (http.Handler, *stubMailbox) {
	t.Helper()
	return newTestHandlerWith(t, nopSender{})
}

func newTestHandlerWith(t *testing.T, sender ports.Sender) (http.Handler, *stubMailbox) {
	t.Helper()
	h, mb, _ := newTestHandlerFull(t, sender)
	return h, mb
}

func newTestHandlerFull(t *testing.T, sender ports.Sender) (http.Handler, *stubMailbox, *stubVacations) {
	t.Helper()
	h, mb, vac, _ := newTestHandlerAll(t, sender)
	return h, mb, vac
}

func newTestHandlerAll(t *testing.T, sender ports.Sender) (http.Handler, *stubMailbox, *stubVacations, *stubAddressBook) {
	t.Helper()
	env := newTestEnv(t, sender)
	return env.h, env.mb, env.vac, env.book
}

// testEnv es el API del webmail con todas sus dependencias falsas a mano de la prueba.
type testEnv struct {
	h        http.Handler
	mb       *stubMailbox
	vac      *stubVacations
	book     *stubAddressBook
	settings *stubSettings
	dav      *stubDAV
}

// testDeps son las dependencias del caso de uso con los stubs dados.
func testDeps(store ports.SessionStore, mb *stubMailbox, sender ports.Sender, vac *stubVacations, book *stubAddressBook, settings *stubSettings, dav *stubDAV) app.Deps {
	return app.Deps{
		Auth: stubAuth{}, Sessions: store, Mail: stubMail{mb: mb},
		Sender: sender, Directory: stubDirectory{}, Vacations: vac, AddressBook: book,
		Signatures: settings, Filters: settings, Passwords: settings, Scheduled: settings, Contacts: dav, Calendar: dav,
		Ledger: &memLedger{m: map[string]domain.SendRecord{}}, Composer: nopComposer{}, Sanitizer: nopSanitizer{}, PartURL: PartURL,
		Logger: zap.NewNop(),
		Config: app.Config{
			CellCode:         testCell,
			Sessions:         domain.SessionPolicy{Idle: 30 * time.Minute, Max: 12 * time.Hour},
			Limits:           domain.Limits{MaxRecipients: 2, MaxMessageBytes: 4096},
			MaxBodyPartBytes: 1024, MaxAttachmentBytes: 1024,
			SendTimeout: 5 * time.Second, MaxScheduledDays: 30, ScheduledPollInterval: time.Minute, ScheduledBatch: 5,
			MaxImportBytes: 512,
		},
	}
}

func newTestEnv(t *testing.T, sender ports.Sender) *testEnv {
	t.Helper()
	env := &testEnv{mb: &stubMailbox{}, vac: &stubVacations{}, book: &stubAddressBook{}, settings: newStubSettings(), dav: &stubDAV{}}
	svc, err := app.New(testDeps(&memStore{m: map[string]domain.Session{}}, env.mb, sender, env.vac, env.book, env.settings, env.dav))
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(svc, Config{
		CookieSecure: true, SessionIdle: 30 * time.Minute, SessionMax: 12 * time.Hour,
		AllowedOrigins:  []string{allowedOrigin, "https://api.example.com"},
		MaxMessageBytes: 4096, OperationTimeout: 5 * time.Second, TransferTimeout: 5 * time.Second,
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	env.h = h.Routes()
	return env
}

func do(h http.Handler, method, path string, body io.Reader, headers map[string]string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func loginBody(user, pass string) io.Reader {
	b, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	return bytes.NewReader(b)
}

func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			return c
		}
	}
	return nil
}

func login(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	rec := do(h, http.MethodPost, BasePath+"/session", loginBody(testUser, testPass), map[string]string{"Origin": allowedOrigin}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	c := sessionCookie(rec)
	if c == nil {
		t.Fatal("login sin cookie")
	}
	return c
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("cuerpo no es JSON: %q", rec.Body.String())
	}
	return env.Error.Code
}

func TestLoginRespondeIgualParaInexistenteYContrasenaMala(t *testing.T) {
	h, _ := newTestHandler(t)
	origin := map[string]string{"Origin": allowedOrigin}
	unknown := do(h, http.MethodPost, BasePath+"/session", loginBody("nadie@empresa.pe", "loquesea-larga"), origin, nil)
	wrong := do(h, http.MethodPost, BasePath+"/session", loginBody(testUser, "mala-contrasena"), origin, nil)

	if unknown.Code != http.StatusUnauthorized || wrong.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d / %d", unknown.Code, wrong.Code)
	}
	if unknown.Body.String() != wrong.Body.String() {
		t.Fatalf("cuerpos distintos: %q / %q", unknown.Body.String(), wrong.Body.String())
	}
	if sessionCookie(unknown) != nil || sessionCookie(wrong) != nil {
		t.Fatal("un rechazo no emite cookie")
	}
	if errorCode(t, wrong) != "INVALID_CREDENTIALS" {
		t.Fatalf("codigo: %s", wrong.Body.String())
	}
}

func TestLoginEmiteCookieSegura(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := do(h, http.MethodPost, BasePath+"/session", loginBody(testUser, testPass), map[string]string{"Origin": allowedOrigin}, nil)
	c := sessionCookie(rec)
	if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != BasePath || c.MaxAge != 12*3600 {
		t.Fatalf("cookie: %+v", c)
	}
	if !strings.HasPrefix(c.Value, testCell+".") || len(c.Value) != len(testCell)+1+43 {
		t.Fatalf("token: la celda y 256 bits en base64url: %q", c.Value)
	}
	if strings.Contains(rec.Body.String(), c.Value) {
		t.Fatal("el token no viaja en el cuerpo")
	}
	hd := rec.Header()
	if hd.Get("Cache-Control") != "no-store" || hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Content-Security-Policy") != apiCSP {
		t.Fatalf("cabeceras: %v", hd)
	}
}

func TestEscriturasExigenOriginPermitido(t *testing.T) {
	h, _ := newTestHandler(t)
	cookie := login(t, h)
	forbidden := []struct {
		method, path, origin string
	}{
		{http.MethodPost, "/session", ""},
		{http.MethodPost, "/session", "https://evil.example.com"},
		{http.MethodPost, "/session", "null"},
		{http.MethodPost, "/session", "https://app.example.com/ruta"},
		{http.MethodPost, "/session", "http://app.example.com"},
		{http.MethodDelete, "/session", "https://evil.example.com"},
		{http.MethodPost, "/send", "https://evil.example.com"},
		{http.MethodPost, "/folders/INBOX/messages/1/flags", "https://app.example.com.evil.test"},
		{http.MethodDelete, "/folders/INBOX/messages/1", ""},
	}
	for _, c := range forbidden {
		rec := do(h, c.method, BasePath+c.path, strings.NewReader("{}"), map[string]string{"Origin": c.origin}, cookie)
		if rec.Code != http.StatusForbidden || errorCode(t, rec) != "ORIGIN_NOT_ALLOWED" {
			t.Errorf("%s %s con Origin %q: %d %s", c.method, c.path, c.origin, rec.Code, rec.Body.String())
		}
	}
	// El puerto por defecto no cambia el origen; una lectura no exige Origin.
	rec := do(h, http.MethodPost, BasePath+"/session", loginBody(testUser, testPass), map[string]string{"Origin": "https://APP.example.com:443"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("origen con puerto por defecto: %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, BasePath+"/folders", nil, map[string]string{"Origin": "https://evil.example.com"}, cookie); rec.Code != http.StatusOK {
		t.Fatalf("una lectura no depende del Origin: %d", rec.Code)
	}
}

func TestRutasExigenSesion(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := do(h, http.MethodGet, BasePath+"/folders", nil, nil, nil)
	if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "SESSION_EXPIRED" {
		t.Fatalf("sin cookie: %d %s", rec.Code, rec.Body.String())
	}
	forged := &http.Cookie{Name: cookieName, Value: strings.Repeat("a", 43)}
	rec = do(h, http.MethodGet, BasePath+"/folders", nil, nil, forged)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("cookie inventada: %d", rec.Code)
	}
	if c := sessionCookie(rec); c == nil || c.MaxAge >= 0 {
		t.Fatalf("una sesion invalida borra la cookie: %+v", c)
	}
}

func TestLogoutCierraLaSesion(t *testing.T) {
	h, _ := newTestHandler(t)
	cookie := login(t, h)
	rec := do(h, http.MethodDelete, BasePath+"/session", nil, map[string]string{"Origin": allowedOrigin}, cookie)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", rec.Code)
	}
	if c := sessionCookie(rec); c == nil || c.MaxAge >= 0 {
		t.Fatalf("el logout borra la cookie: %+v", c)
	}
	if rec := do(h, http.MethodGet, BasePath+"/folders", nil, nil, cookie); rec.Code != http.StatusUnauthorized {
		t.Fatalf("la cookie vieja ya no vale: %d", rec.Code)
	}
}

func TestCarpetaCodificadaSeDecodificaYValida(t *testing.T) {
	h, mb := newTestHandler(t)
	cookie := login(t, h)
	rec := do(h, http.MethodGet, BasePath+"/folders/INBOX%2FProyectos%20Q3/messages", nil, nil, cookie)
	if rec.Code != http.StatusOK || mb.listed != "INBOX/Proyectos Q3" {
		t.Fatalf("status=%d carpeta=%q", rec.Code, mb.listed)
	}
	rec = do(h, http.MethodGet, BasePath+"/folders/INBOX%0D%0AA1%20DELETE/messages", nil, nil, cookie)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("carpeta con CRLF: %d", rec.Code)
	}
	rec = do(h, http.MethodGet, BasePath+"/folders/INBOX/messages/0", nil, nil, cookie)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("uid invalido: %d", rec.Code)
	}
}

func TestDescargaDeParteNuncaSeRenderiza(t *testing.T) {
	h, mb := newTestHandler(t)
	cookie := login(t, h)
	mb.part = domain.Part{ID: "2", ContentType: "text/html", Filename: "../../factura\".html"}
	mb.body = "<script>alert(1)</script>"
	rec := do(h, http.MethodGet, BasePath+"/folders/INBOX/messages/5/parts/2", nil, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	hd := rec.Header()
	if hd.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("un HTML adjunto se entrega como binario: %q", hd.Get("Content-Type"))
	}
	if cd := hd.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, "factura_.html") || strings.Contains(cd, "..") {
		t.Fatalf("Content-Disposition: %q", cd)
	}
	if hd.Get("Content-Security-Policy") != partCSP || hd.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("cabeceras: %v", hd)
	}
	if rec.Body.String() != mb.body {
		t.Fatal("el contenido llega intacto")
	}
	if rec := do(h, http.MethodGet, BasePath+"/folders/INBOX/messages/5/parts/1..2", nil, nil, cookie); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("parte invalida: %d", rec.Code)
	}
}

func multipartBody(t *testing.T, fields map[string]string, fileSize int) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if fileSize > 0 {
		fw, err := w.CreateFormFile(attachmentField, "grande.bin")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(make([]byte, fileSize))
	}
	_ = w.Close()
	return &buf, w.FormDataContentType()
}

func TestEnvioAplicaLimitesYRechazaInyeccion(t *testing.T) {
	h, _ := newTestHandler(t)
	cookie := login(t, h)
	cases := []struct {
		name     string
		fields   map[string]string
		fileSize int
		status   int
		code     string
	}{
		{"demasiados destinatarios", map[string]string{"to": "a@x.com, b@x.com, c@x.com", "text": "x"}, 0, http.StatusUnprocessableEntity, "TOO_MANY_RECIPIENTS"},
		{"asunto con CRLF", map[string]string{"to": "a@x.com", "subject": "Hola\r\nBcc: espia@x.com"}, 0, http.StatusUnprocessableEntity, "VALIDATION_ERROR"},
		{"destinatario con CRLF", map[string]string{"to": "a@x.com\r\nBcc: espia@x.com"}, 0, http.StatusUnprocessableEntity, "VALIDATION_ERROR"},
		{"campo desconocido", map[string]string{"to": "a@x.com", "x-mailer": "yo"}, 0, http.StatusUnprocessableEntity, "VALIDATION_ERROR"},
		{"adjunto demasiado grande", map[string]string{"to": "a@x.com"}, 5000, http.StatusRequestEntityTooLarge, "MESSAGE_TOO_LARGE"},
	}
	for _, c := range cases {
		body, ctype := multipartBody(t, c.fields, c.fileSize)
		rec := do(h, http.MethodPost, BasePath+"/send", body, sendHeaders(ctype), cookie)
		if rec.Code != c.status || errorCode(t, rec) != c.code {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body.String())
		}
	}
	body, ctype := multipartBody(t, map[string]string{"to": "a@x.com", "subject": "Hola", "text": "cuerpo"}, 10)
	rec := do(h, http.MethodPost, BasePath+"/send", body, sendHeaders(ctype), cookie)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("envio valido: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(h, http.MethodPost, BasePath+"/send", strings.NewReader(`{"to":"a@x.com"}`), sendHeaders("application/json"), cookie)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("un envio que no es multipart se rechaza: %d", rec.Code)
	}
}

// Un token de otra celda (el prefijo cambiado de uno valido, o uno de otra instancia) no abre
// nada aqui: 401 como una sesion caducada y la cookie se borra. Cerrar sesion con el solo la
// borra. Un token sin celda, el formato anterior, tampoco vale.
func TestTokenDeOtraCeldaSeRechazaYBorraLaCookie(t *testing.T) {
	h, _ := newTestHandler(t)
	valid := login(t, h)
	_, secret, _ := strings.Cut(valid.Value, ".")
	for caso, value := range map[string]string{
		"prefijo de otra celda": "pe-02." + secret,
		"celda mal formada":     "PE-01." + secret,
		"sin celda":             secret,
	} {
		rec := do(h, http.MethodGet, BasePath+"/session", nil, nil, &http.Cookie{Name: cookieName, Value: value})
		if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "SESSION_EXPIRED" {
			t.Fatalf("%s: %d %s", caso, rec.Code, rec.Body)
		}
		if c := sessionCookie(rec); c == nil || c.MaxAge >= 0 {
			t.Fatalf("%s: la cookie se borra: %+v", caso, c)
		}
	}
	rec := do(h, http.MethodDelete, BasePath+"/session", nil, map[string]string{"Origin": allowedOrigin}, &http.Cookie{Name: cookieName, Value: "pe-02." + secret})
	if rec.Code != http.StatusNoContent || sessionCookie(rec) == nil || sessionCookie(rec).MaxAge >= 0 {
		t.Fatalf("cerrar sesion con un token de otra celda: %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, BasePath+"/session", nil, nil, valid); rec.Code != http.StatusOK {
		t.Fatalf("la sesion real sigue abierta: %d", rec.Code)
	}
}
