package http

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/app"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

const proxyMailbox = "22222222-2222-4222-8222-222222222222"

var (
	proxyKey = domain.DeriveRemoteImageKey([]byte("secreto-de-pruebas-de-32-caracteres"))
	gifBytes = []byte("GIF89a\x01\x00\x01\x00resto")
)

type stubFetcher struct {
	mu    sync.Mutex
	data  []byte
	err   error
	calls int
	// block, si no es nil, retiene la descarga hasta que se cierra.
	block chan struct{}
	// started avisa de que una descarga esta en curso.
	started chan struct{}
}

func (f *stubFetcher) Fetch(ctx context.Context, _ string) ([]byte, error) {
	f.mu.Lock()
	f.calls++
	block, started := f.block, f.started
	f.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.data, f.err
}

// keyLimiter rechaza las claves de denied y apunta todas las que se consultan.
type keyLimiter struct {
	mu     sync.Mutex
	denied map[string]bool
	keys   []string
}

func (l *keyLimiter) AllowIP(context.Context, string) (bool, time.Duration) { return true, 0 }
func (l *keyLimiter) AllowKey(_ context.Context, key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keys = append(l.keys, key)
	return !l.denied[key], 1500 * time.Millisecond
}

// denyAll rechaza todo por IP.
type denyAll struct{}

func (denyAll) AllowIP(context.Context, string) (bool, time.Duration)  { return false, time.Second }
func (denyAll) AllowKey(context.Context, string) (bool, time.Duration) { return false, time.Second }

type countingMetrics struct {
	mu       sync.Mutex
	outcomes map[string]int
}

func (m *countingMetrics) ImageProxyRequest(outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outcomes[outcome]++
}

type proxyEnv struct {
	h       http.Handler
	fetcher *stubFetcher
	limiter *keyLimiter
	metrics *countingMetrics
}

func newProxyEnv(t *testing.T, tune func(*Config)) *proxyEnv {
	t.Helper()
	env := &proxyEnv{fetcher: &stubFetcher{data: gifBytes}, limiter: &keyLimiter{denied: map[string]bool{}},
		metrics: &countingMetrics{outcomes: map[string]int{}}}
	settings := newStubSettings()
	deps := testDeps(&memStore{m: map[string]domain.Session{}}, &stubMailbox{}, nopSender{}, &stubVacations{}, &stubAddressBook{}, settings, &stubDAV{})
	deps.ImageProxy = &app.ImageProxyDeps{Fetcher: env.fetcher, URL: RemoteImageURL, Key: proxyKey, TTL: time.Hour}
	svc, err := app.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		CookieSecure: true, SessionIdle: 30 * time.Minute, SessionMax: 12 * time.Hour,
		MFAChallengeTTL: 5 * time.Minute, IPRateLimiter: unlimited{}, MailboxRateLimiter: unlimited{},
		ImageProxyRateLimiter: env.limiter, ImageProxyMetrics: env.metrics,
		AllowedOrigins:  []string{allowedOrigin},
		MaxMessageBytes: 4096, OperationTimeout: 5 * time.Second, TransferTimeout: 5 * time.Second,
	}
	if tune != nil {
		tune(&cfg)
	}
	h, err := NewHandler(svc, cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	env.h = h.Routes()
	return env
}

func signedPath(t *testing.T, rawURL string, now time.Time) string {
	t.Helper()
	link, err := domain.NewRemoteImageLink(rawURL, proxyMailbox, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return RemoteImageURL(link.Sign(proxyKey))
}

func TestProxyServeLaImagenFirmadaSinSesionYConCabecerasSeguras(t *testing.T) {
	env := newProxyEnv(t, nil)
	path := signedPath(t, "https://x.test/logo.gif", time.Now())
	if !strings.HasPrefix(path, BasePath+ImageProxyPath+"?") {
		t.Fatalf("ruta relativa al origen de la aplicacion: %s", path)
	}
	rec := do(env.h, http.MethodGet, path, nil, nil, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != string(gifBytes) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	hd := rec.Header()
	for name, want := range map[string]string{
		"Content-Type": "image/gif", "X-Content-Type-Options": "nosniff", "Content-Disposition": "inline",
		"Cross-Origin-Resource-Policy": "cross-origin", "Referrer-Policy": "no-referrer",
		"Content-Length": strconv.Itoa(len(gifBytes)),
	} {
		if hd.Get(name) != want {
			t.Errorf("%s: %q", name, hd.Get(name))
		}
	}
	if csp := hd.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "sandbox") {
		t.Errorf("CSP: %q", csp)
	}
	cc := hd.Get("Cache-Control")
	secs, err := strconv.Atoi(strings.TrimPrefix(cc, "private, max-age="))
	if !strings.HasPrefix(cc, "private, max-age=") || err != nil || secs <= 0 || secs > int((time.Hour+15*time.Minute)/time.Second) {
		t.Errorf("Cache-Control acotado por la caducidad: %q", cc)
	}
	if hd.Get("Pragma") != "" || hd.Get("Set-Cookie") != "" {
		t.Errorf("ni Pragma ni cookies: %v", hd)
	}
	if len(env.limiter.keys) != 1 || env.limiter.keys[0] != proxyMailbox || env.metrics.outcomes[domain.RemoteImageOutcomeOK] != 1 {
		t.Fatalf("cupo del buzon del enlace y metrica: %v %v", env.limiter.keys, env.metrics.outcomes)
	}
}

func TestProxyRechazaEnlacesAlteradosDeOtroBuzonOCaducados(t *testing.T) {
	env := newProxyEnv(t, nil)
	good := signedPath(t, "https://x.test/logo.gif", time.Now())
	u, _ := url.Parse(good)

	mutate := func(key, value string) string {
		q := u.Query()
		q.Set(key, value)
		return u.Path + "?" + q.Encode()
	}
	for name, path := range map[string]string{
		"otro buzon":   mutate("m", "33333333-3333-4333-8333-333333333333"),
		"otra url":     mutate("u", "aHR0cHM6Ly94LnRlc3QvYi5naWY"),
		"otra firma":   mutate("s", strings.Repeat("A", 43)),
		"mas vida":     mutate("x", "9999999999"),
		"sin nada":     BasePath + ImageProxyPath,
		"sin firma":    mutate("s", ""),
		"buzon no id":  mutate("m", "ana@x.test"),
		"firma basura": mutate("s", "%%%"),
	} {
		rec := do(env.h, http.MethodGet, path, nil, nil, nil)
		if rec.Code != http.StatusForbidden || errorCode(t, rec) != "IMAGE_LINK_INVALID" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	expired := signedPath(t, "https://x.test/logo.gif", time.Now().Add(-3*time.Hour))
	if rec := do(env.h, http.MethodGet, expired, nil, nil, nil); rec.Code != http.StatusGone || errorCode(t, rec) != "IMAGE_LINK_EXPIRED" {
		t.Fatalf("caducado: %d %s", rec.Code, rec.Body.String())
	}
	if env.fetcher.calls != 0 || len(env.limiter.keys) != 0 {
		t.Fatalf("un enlace que no vale no descarga ni gasta cupo: %d %v", env.fetcher.calls, env.limiter.keys)
	}
	if env.metrics.outcomes[domain.RemoteImageOutcomeInvalid] != 8 || env.metrics.outcomes[domain.RemoteImageOutcomeExpired] != 1 {
		t.Fatalf("metricas: %v", env.metrics.outcomes)
	}
}

func TestProxyNoEntregaLoQueNoEsUnMapaDeBits(t *testing.T) {
	env := newProxyEnv(t, nil)
	path := signedPath(t, "https://x.test/a", time.Now())
	for name, data := range map[string]string{
		"svg":  `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`,
		"html": "<html><script>alert(1)</script></html>",
	} {
		env.fetcher.data = []byte(data)
		rec := do(env.h, http.MethodGet, path, nil, nil, nil)
		if rec.Code != http.StatusBadGateway || errorCode(t, rec) != "IMAGE_TYPE_NOT_ALLOWED" || strings.Contains(rec.Body.String(), "alert") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "image/") {
			t.Errorf("%s: %s", name, ct)
		}
	}
}

func TestProxyTraduceLosFallosDelServidorRemotoSinDetalles(t *testing.T) {
	env := newProxyEnv(t, nil)
	path := signedPath(t, "https://x.test/a.gif?token=secreto", time.Now())
	for _, c := range []struct {
		err     error
		code    string
		outcome string
	}{
		{domain.ErrRemoteImageRefused, "IMAGE_UNAVAILABLE", domain.RemoteImageOutcomeRefused},
		{errors.Join(domain.ErrRemoteImageUnavailable, errors.New("dial 10.0.0.1 secreto")), "IMAGE_UNAVAILABLE", domain.RemoteImageOutcomeUpstream},
		{domain.ErrRemoteImageTooLarge, "IMAGE_TOO_LARGE", domain.RemoteImageOutcomeTooLarge},
		{errors.New("inesperado secreto"), "IMAGE_UNAVAILABLE", domain.RemoteImageOutcomeUpstream},
	} {
		env.fetcher.err = c.err
		rec := do(env.h, http.MethodGet, path, nil, nil, nil)
		if rec.Code != http.StatusBadGateway || errorCode(t, rec) != c.code || strings.Contains(rec.Body.String(), "secreto") {
			t.Errorf("%v: %d %s", c.err, rec.Code, rec.Body.String())
		}
	}
	if env.metrics.outcomes[domain.RemoteImageOutcomeUpstream] != 2 || env.metrics.outcomes[domain.RemoteImageOutcomeRefused] != 1 {
		t.Fatalf("metricas: %v", env.metrics.outcomes)
	}
}

func TestProxyCupoPorBuzonYPorIP(t *testing.T) {
	env := newProxyEnv(t, nil)
	env.limiter.denied[proxyMailbox] = true
	path := signedPath(t, "https://x.test/a.gif", time.Now())
	rec := do(env.h, http.MethodGet, path, nil, nil, nil)
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "RATE_LIMITED" || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("cupo del buzon: %d %s %q", rec.Code, rec.Body.String(), rec.Header().Get("Retry-After"))
	}
	if env.fetcher.calls != 0 || env.metrics.outcomes[domain.RemoteImageOutcomeRateLimited] != 1 {
		t.Fatalf("sin cupo no se descarga: %d %v", env.fetcher.calls, env.metrics.outcomes)
	}

	byIP := newProxyEnv(t, func(c *Config) { c.IPRateLimiter = denyAll{} })
	rec = do(byIP.h, http.MethodGet, path, nil, nil, nil)
	if rec.Code != http.StatusTooManyRequests || byIP.fetcher.calls != 0 || len(byIP.limiter.keys) != 0 {
		t.Fatalf("el cupo por IP va antes que nada: %d", rec.Code)
	}
}

func TestProxyTopeDeDescargasSimultaneas(t *testing.T) {
	env := newProxyEnv(t, func(c *Config) { c.ImageProxyConcurrency = 1 })
	env.fetcher.block = make(chan struct{})
	env.fetcher.started = make(chan struct{}, 1)
	path := signedPath(t, "https://x.test/a.gif", time.Now())
	done := make(chan int)
	go func() { done <- do(env.h, http.MethodGet, path, nil, nil, nil).Code }()
	<-env.fetcher.started
	rec := do(env.h, http.MethodGet, path, nil, nil, nil)
	if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec) != "IMAGE_PROXY_BUSY" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("lleno: %d %s", rec.Code, rec.Body.String())
	}
	close(env.fetcher.block)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("la primera termina: %d", code)
	}
	env.fetcher.started = nil
	if rec := do(env.h, http.MethodGet, path, nil, nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("el hueco se libera: %d", rec.Code)
	}
	if env.metrics.outcomes[domain.RemoteImageOutcomeBusy] != 1 {
		t.Fatalf("metricas: %v", env.metrics.outcomes)
	}
}

func TestProxySoloGET(t *testing.T) {
	env := newProxyEnv(t, nil)
	path := signedPath(t, "https://x.test/a.gif", time.Now())
	rec := do(env.h, http.MethodPost, path, nil, map[string]string{"Origin": allowedOrigin}, nil)
	if rec.Code != http.StatusMethodNotAllowed || env.fetcher.calls != 0 {
		t.Fatalf("POST: %d", rec.Code)
	}
}

func TestHandlerExigeElLimitadorDelProxy(t *testing.T) {
	svc, err := app.New(testDeps(&memStore{m: map[string]domain.Session{}}, &stubMailbox{}, nopSender{}, &stubVacations{}, &stubAddressBook{}, newStubSettings(), &stubDAV{}))
	if err != nil {
		t.Fatal(err)
	}
	base := Config{
		CookieSecure: true, SessionIdle: time.Minute, SessionMax: time.Hour, MFAChallengeTTL: time.Minute,
		IPRateLimiter: unlimited{}, MailboxRateLimiter: unlimited{}, AllowedOrigins: []string{allowedOrigin},
		MaxMessageBytes: 1, OperationTimeout: time.Second, TransferTimeout: time.Second,
	}
	if _, err := NewHandler(svc, base, zap.NewNop()); err == nil {
		t.Fatal("sin limitador del proxy no arranca")
	}
	base.ImageProxyRateLimiter = unlimited{}
	base.ImageProxyConcurrency = -1
	if _, err := NewHandler(svc, base, zap.NewNop()); err == nil {
		t.Fatal("concurrencia negativa")
	}
}
