package sns

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"
)

const testCertURL = "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-9c6465fa7f48f5cacd23014631ec1136.pem"

// testCert genera un certificado autofirmado con su clave, en PEM.
func testCert(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "sns.amazonaws.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func sign(t *testing.T, key *rsa.PrivateKey, e *Envelope, version string) {
	t.Helper()
	e.SignatureVersion = version
	canonical, err := canonicalString(e)
	if err != nil {
		t.Fatal(err)
	}
	hash := crypto.SHA256
	if version == "1" {
		hash = crypto.SHA1
	}
	h := hash.New()
	h.Write(canonical)
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, hash, h.Sum(nil))
	if err != nil {
		t.Fatal(err)
	}
	e.Signature = base64.StdEncoding.EncodeToString(sig)
}

// fakeFetcher sirve recursos por URL y cuenta las descargas.
type fakeFetcher struct {
	mu     sync.Mutex
	served map[string][]byte
	calls  map[string]int
}

func newFakeFetcher(served map[string][]byte) *fakeFetcher {
	return &fakeFetcher{served: served, calls: map[string]int{}}
}

func (f *fakeFetcher) fetch(_ context.Context, rawURL string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[rawURL]++
	if b, ok := f.served[rawURL]; ok {
		return b, nil
	}
	return nil, errors.New("no servido")
}

func (f *fakeFetcher) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += c
	}
	return n
}

func notification() *Envelope {
	return &Envelope{
		Type:           TypeNotification,
		MessageID:      "22b80b92-fdea-4c2c-8f9d-bdfb0c7bf324",
		TopicArn:       "arn:aws:sns:us-east-1:123456789012:cfm-transactional-events",
		Message:        `{"eventType":"Delivery","mail":{"messageId":"x"}}`,
		Timestamp:      "2026-09-12T12:00:00.000Z",
		SigningCertURL: testCertURL,
	}
}

func TestVerifyValidSignature(t *testing.T) {
	key, certPEM := testCert(t)
	for _, version := range []string{"1", "2"} {
		for _, subject := range []string{"", "Amazon SES Email Event Notification"} {
			f := newFakeFetcher(map[string][]byte{testCertURL: certPEM})
			v := NewVerifierWithFetcher(f.fetch)
			e := notification()
			e.Subject = subject
			sign(t, key, e, version)
			if err := v.Verify(context.Background(), e); err != nil {
				t.Errorf("version %s, subject %q: firma valida rechazada: %v", version, subject, err)
			}
		}
	}
}

func TestVerifySubscriptionConfirmation(t *testing.T) {
	key, certPEM := testCert(t)
	v := NewVerifierWithFetcher(newFakeFetcher(map[string][]byte{testCertURL: certPEM}).fetch)
	e := &Envelope{
		Type:           TypeSubscriptionConfirmation,
		MessageID:      "165545c9-2a5c-472c-8df2-7ff2be2b3b1b",
		Token:          "2336412f37fb687f5d51e6e241d09c805a5a57b30d712f794cc5f6a988666d92768dd60a747ba6f3beb71854e285d6ad02428b09ceece29417f1f02d609c582afbacc99c583a916b9981dd2728f4ae6fdb82efd087cc3b7849e05798d2d2785c03b0879594eeac82c01f235d0e717736",
		TopicArn:       "arn:aws:sns:us-east-1:123456789012:cfm-transactional-events",
		Message:        "You have chosen to subscribe to the topic.",
		SubscribeURL:   "https://sns.us-east-1.amazonaws.com/?Action=ConfirmSubscription&TopicArn=arn:aws:sns:us-east-1:123456789012:cfm&Token=2336412f",
		Timestamp:      "2026-09-12T12:00:00.000Z",
		SigningCertURL: testCertURL,
	}
	sign(t, key, e, "1")
	if err := v.Verify(context.Background(), e); err != nil {
		t.Fatalf("confirmacion firmada rechazada: %v", err)
	}
	e.Token = "otro"
	if err := v.Verify(context.Background(), e); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("un Token alterado debe invalidar la firma, err = %v", err)
	}
}

func TestVerifyRejectsTamperedFields(t *testing.T) {
	key, certPEM := testCert(t)
	v := NewVerifierWithFetcher(newFakeFetcher(map[string][]byte{testCertURL: certPEM}).fetch)
	tamper := map[string]func(e *Envelope){
		"message":   func(e *Envelope) { e.Message = `{"eventType":"Bounce"}` },
		"messageid": func(e *Envelope) { e.MessageID = "otro" },
		"topic":     func(e *Envelope) { e.TopicArn = "arn:aws:sns:us-east-1:999999999999:ajeno" },
		"timestamp": func(e *Envelope) { e.Timestamp = "2026-09-13T12:00:00.000Z" },
		"subject":   func(e *Envelope) { e.Subject = "anadido" },
		"version":   func(e *Envelope) { e.SignatureVersion = "1" },
		"signature": func(e *Envelope) { e.Signature = "no-es-base64!" },
	}
	for name, fn := range tamper {
		e := notification()
		sign(t, key, e, "2")
		fn(e)
		if err := v.Verify(context.Background(), e); !errors.Is(err, ErrInvalidSignature) {
			t.Errorf("%s alterado: se esperaba ErrInvalidSignature, err = %v", name, err)
		}
	}
}

func TestVerifyRejectsForeignKey(t *testing.T) {
	_, certPEM := testCert(t)
	otherKey, _ := testCert(t)
	v := NewVerifierWithFetcher(newFakeFetcher(map[string][]byte{testCertURL: certPEM}).fetch)
	e := notification()
	sign(t, otherKey, e, "2")
	if err := v.Verify(context.Background(), e); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("firmado con otra clave: err = %v", err)
	}
}

func TestVerifyRejectsCertURLNotAllowed(t *testing.T) {
	key, certPEM := testCert(t)
	urls := []string{
		"http://sns.us-east-1.amazonaws.com/SimpleNotificationService-abc.pem",
		"https://sns.us-east-1.amazonaws.com.evil.example/SimpleNotificationService-abc.pem",
		"https://sns.us-east-1.amazonaws.com@evil.example/SimpleNotificationService-abc.pem",
		"https://sns.us-east-1.amazonaws.com:8443/SimpleNotificationService-abc.pem",
		"https://evil.example/SimpleNotificationService-abc.pem",
		"https://s3.amazonaws.com/SimpleNotificationService-abc.pem",
		"https://sns.us-east-1.amazonaws.com/otro.pem",
		"https://sns.us-east-1.amazonaws.com/SimpleNotificationService-abc.pem?x=1",
		"",
	}
	for _, u := range urls {
		f := newFakeFetcher(map[string][]byte{u: certPEM})
		v := NewVerifierWithFetcher(f.fetch)
		e := notification()
		e.SigningCertURL = u
		sign(t, key, e, "2")
		if err := v.Verify(context.Background(), e); !errors.Is(err, ErrCertURL) {
			t.Errorf("%q: se esperaba ErrCertURL, err = %v", u, err)
		}
		if f.total() != 0 {
			t.Errorf("%q: no debe descargarse nada de un host no permitido", u)
		}
	}
}

func TestVerifyAcceptsOtherRegions(t *testing.T) {
	for _, u := range []string{
		"https://sns.eu-west-1.amazonaws.com/SimpleNotificationService-abc.pem",
		"https://sns.ap-southeast-2.amazonaws.com/SimpleNotificationService-abc.pem",
		"https://sns.us-gov-west-1.amazonaws.com/SimpleNotificationService-abc.pem",
	} {
		if !certURLPattern.MatchString(u) {
			t.Errorf("%q deberia admitirse", u)
		}
	}
}

func TestVerifyCachesCertificate(t *testing.T) {
	key, certPEM := testCert(t)
	f := newFakeFetcher(map[string][]byte{testCertURL: certPEM})
	v := NewVerifierWithFetcher(f.fetch)
	for i := 0; i < 3; i++ {
		e := notification()
		sign(t, key, e, "2")
		if err := v.Verify(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	if f.calls[testCertURL] != 1 {
		t.Fatalf("el certificado debe descargarse una vez, se descargo %d", f.calls[testCertURL])
	}
}

func TestVerifyRejectsUnknownVersionAndType(t *testing.T) {
	key, certPEM := testCert(t)
	v := NewVerifierWithFetcher(newFakeFetcher(map[string][]byte{testCertURL: certPEM}).fetch)
	e := notification()
	sign(t, key, e, "2")
	e.SignatureVersion = "3"
	if err := v.Verify(context.Background(), e); !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("SignatureVersion desconocida: err = %v", err)
	}
	e = notification()
	e.Type = "Otro"
	if err := v.Verify(context.Background(), e); !errors.Is(err, ErrUnknownType) {
		t.Errorf("tipo desconocido: err = %v", err)
	}
}

func TestConfirmSubscriptionGuardsSSRF(t *testing.T) {
	allowed := "https://sns.us-east-1.amazonaws.com/?Action=ConfirmSubscription&Token=abc"
	f := newFakeFetcher(map[string][]byte{allowed: []byte("<ConfirmSubscriptionResponse/>")})
	v := NewVerifierWithFetcher(f.fetch)
	if err := v.ConfirmSubscription(context.Background(), allowed); err != nil {
		t.Fatalf("SubscribeURL legitima rechazada: %v", err)
	}
	for _, u := range []string{
		"http://sns.us-east-1.amazonaws.com/?Action=ConfirmSubscription",
		"https://169.254.169.254/latest/meta-data/",
		"https://sns.us-east-1.amazonaws.com.evil.example/?Action=ConfirmSubscription",
		"https://sns.us-east-1.amazonaws.com:444/?Action=ConfirmSubscription",
		"https://localhost/?Action=ConfirmSubscription",
		"https://user@sns-us-east-1.amazonaws.com/",
		"gopher://sns.us-east-1.amazonaws.com/",
	} {
		if err := v.ConfirmSubscription(context.Background(), u); !errors.Is(err, ErrSubscribeURL) {
			t.Errorf("%q: se esperaba ErrSubscribeURL, err = %v", u, err)
		}
	}
	if f.total() != 1 {
		t.Fatalf("solo la URL legitima debe visitarse, visitas = %d", f.total())
	}
}
