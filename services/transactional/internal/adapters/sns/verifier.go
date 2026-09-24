// Package sns verifica la firma de las notificaciones de Amazon SNS que transportan los
// eventos de SES y confirma las suscripciones. Es la unica autenticacion del webhook
// publico: sin firma valida no se toca la base.
package sns

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Tipos de mensaje SNS.
const (
	TypeNotification             = "Notification"
	TypeSubscriptionConfirmation = "SubscriptionConfirmation"
	TypeUnsubscribeConfirmation  = "UnsubscribeConfirmation"
)

// Envelope es el cuerpo JSON que SNS entrega por HTTPS.
type Envelope struct {
	Type             string `json:"Type"`
	MessageID        string `json:"MessageId"`
	TopicArn         string `json:"TopicArn"`
	Subject          string `json:"Subject"`
	Message          string `json:"Message"`
	Timestamp        string `json:"Timestamp"`
	SignatureVersion string `json:"SignatureVersion"`
	Signature        string `json:"Signature"`
	SigningCertURL   string `json:"SigningCertURL"`
	SubscribeURL     string `json:"SubscribeURL"`
	Token            string `json:"Token"`
	UnsubscribeURL   string `json:"UnsubscribeURL"`
}

var (
	ErrInvalidSignature = errors.New("sns: firma invalida")
	ErrCertURL          = errors.New("sns: SigningCertURL no permitida")
	ErrSubscribeURL     = errors.New("sns: SubscribeURL no permitida")
	ErrUnknownType      = errors.New("sns: tipo de mensaje desconocido")
)

// El certificado solo puede venir del propio SNS de una region de AWS, por https. La
// expresion es estricta a proposito: un host parecido (sns.us-east-1.amazonaws.com.evil)
// no pasa.
var (
	certURLPattern       = regexp.MustCompile(`^https://sns\.[a-z]{2}(?:-gov)?-[a-z]+-\d\.amazonaws\.com/SimpleNotificationService-[A-Za-z0-9]{1,64}\.pem$`)
	subscribeHostPattern = regexp.MustCompile(`^sns\.[a-z]{2}(?:-gov)?-[a-z]+-\d\.amazonaws\.com$`)
)

// maxCertBytes acota la descarga del certificado.
const maxCertBytes = 64 << 10

// Cache de certificados. Una descarga fallida se recuerda failedCertTTL: sin eso, quien conozca
// el ARN del topic fuerza una salida de red por peticion cambiando el nombre del fichero. Las dos
// tablas se acotan a maxCachedCerts; al llenarse se vacian (los certificados reales son pocos).
const (
	failedCertTTL  = 5 * time.Minute
	maxCachedCerts = 64
)

// Fetcher descarga un recurso https; se sustituye en pruebas.
type Fetcher func(ctx context.Context, rawURL string) ([]byte, error)

type Verifier struct {
	fetch Fetcher
	http  *http.Client

	// region, si se fija, es la unica de la que se acepta el certificado: la del topic.
	region string

	mu     sync.Mutex
	certs  map[string]*x509.Certificate
	failed map[string]time.Time
	now    func() time.Time
}

func NewVerifier() *Verifier {
	v := &Verifier{
		http: &http.Client{
			Timeout:       10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		certs:  make(map[string]*x509.Certificate),
		failed: make(map[string]time.Time),
		now:    time.Now,
	}
	v.fetch = v.httpFetch
	return v
}

// ForTopic limita el certificado a la region del topic (arn:aws:sns:<region>:<cuenta>:<nombre>).
// Un ARN que no tiene esa forma no restringe nada y se informa como error.
func (v *Verifier) ForTopic(topicARN string) (*Verifier, error) {
	region, err := RegionFromTopicARN(topicARN)
	if err != nil {
		return v, err
	}
	v.region = region
	return v, nil
}

var topicARNPattern = regexp.MustCompile(`^arn:aws(?:-[a-z]+)*:sns:([a-z]{2}(?:-gov)?-[a-z]+-\d):\d{12}:[A-Za-z0-9_-]{1,256}$`)

// RegionFromTopicARN devuelve la region de un ARN de topic SNS.
func RegionFromTopicARN(topicARN string) (string, error) {
	m := topicARNPattern.FindStringSubmatch(topicARN)
	if m == nil {
		return "", fmt.Errorf("sns: ARN de topic no valido: %q", topicARN)
	}
	return m[1], nil
}

// NewVerifierWithFetcher permite inyectar la descarga (pruebas).
func NewVerifierWithFetcher(f Fetcher) *Verifier {
	v := NewVerifier()
	v.fetch = f
	return v
}

func (v *Verifier) httpFetch(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sns: descarga del certificado respondio %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxCertBytes))
}

// Verify comprueba la firma del sobre con el certificado de SNS.
func (v *Verifier) Verify(ctx context.Context, e *Envelope) error {
	canonical, err := canonicalString(e)
	if err != nil {
		return err
	}
	var algo x509.SignatureAlgorithm
	switch e.SignatureVersion {
	case "1":
		algo = x509.SHA1WithRSA
	case "2":
		algo = x509.SHA256WithRSA
	default:
		return fmt.Errorf("%w: SignatureVersion %q", ErrInvalidSignature, e.SignatureVersion)
	}
	signature, err := base64.StdEncoding.DecodeString(e.Signature)
	if err != nil {
		return fmt.Errorf("%w: firma no es base64", ErrInvalidSignature)
	}
	cert, err := v.certificate(ctx, e.SigningCertURL)
	if err != nil {
		return err
	}
	if err := checkSignature(cert, algo, canonical, signature); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSignature, err)
	}
	return nil
}

// checkSignature verifica PKCS#1 v1.5 con la clave RSA del certificado. Se hace a mano y
// no con cert.CheckSignature porque x509 rechaza SHA1 como inseguro, y SignatureVersion
// 1 (SHA1) sigue siendo el valor por defecto con el que SNS firma.
func checkSignature(cert *x509.Certificate, algo x509.SignatureAlgorithm, signed, signature []byte) error {
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return errors.New("el certificado no lleva una clave RSA")
	}
	var hash crypto.Hash
	switch algo {
	case x509.SHA1WithRSA:
		hash = crypto.SHA1
	case x509.SHA256WithRSA:
		hash = crypto.SHA256
	default:
		return errors.New("algoritmo no admitido")
	}
	h := hash.New()
	h.Write(signed)
	return rsa.VerifyPKCS1v15(pub, hash, h.Sum(nil), signature)
}

// canonicalString reconstruye la cadena firmada con el orden de campos que documenta AWS.
func canonicalString(e *Envelope) ([]byte, error) {
	var pairs [][2]string
	switch e.Type {
	case TypeNotification:
		pairs = [][2]string{{"Message", e.Message}, {"MessageId", e.MessageID}}
		if e.Subject != "" {
			pairs = append(pairs, [2]string{"Subject", e.Subject})
		}
		pairs = append(pairs, [2]string{"Timestamp", e.Timestamp}, [2]string{"TopicArn", e.TopicArn}, [2]string{"Type", e.Type})
	case TypeSubscriptionConfirmation, TypeUnsubscribeConfirmation:
		pairs = [][2]string{
			{"Message", e.Message}, {"MessageId", e.MessageID}, {"SubscribeURL", e.SubscribeURL},
			{"Timestamp", e.Timestamp}, {"Token", e.Token}, {"TopicArn", e.TopicArn}, {"Type", e.Type},
		}
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownType, e.Type)
	}
	var out []byte
	for _, p := range pairs {
		out = append(out, p[0]...)
		out = append(out, '\n')
		out = append(out, p[1]...)
		out = append(out, '\n')
	}
	return out, nil
}

func (v *Verifier) certificate(ctx context.Context, rawURL string) (*x509.Certificate, error) {
	if !certURLPattern.MatchString(rawURL) {
		return nil, ErrCertURL
	}
	if v.region != "" && !strings.HasPrefix(rawURL, "https://sns."+v.region+".amazonaws.com/") {
		return nil, fmt.Errorf("%w: el certificado no es de la región del topic (%s)", ErrCertURL, v.region)
	}
	now := v.now()
	v.mu.Lock()
	cert, ok := v.certs[rawURL]
	if ok && now.After(cert.NotAfter) {
		delete(v.certs, rawURL)
		ok = false
	}
	until, recentlyFailed := v.failed[rawURL]
	if recentlyFailed && now.After(until) {
		delete(v.failed, rawURL)
		recentlyFailed = false
	}
	v.mu.Unlock()
	if ok {
		return cert, nil
	}
	if recentlyFailed {
		return nil, errors.New("sns: certificado no disponible (fallo reciente)")
	}
	cert, err := v.load(ctx, rawURL, now)
	if err != nil {
		v.remember(rawURL, now.Add(failedCertTTL))
		return nil, err
	}
	v.mu.Lock()
	if len(v.certs) >= maxCachedCerts {
		v.certs = make(map[string]*x509.Certificate)
	}
	v.certs[rawURL] = cert
	v.mu.Unlock()
	return cert, nil
}

func (v *Verifier) remember(key string, until time.Time) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.failed) >= maxCachedCerts {
		v.failed = make(map[string]time.Time)
	}
	v.failed[key] = until
}

// load descarga y valida el certificado.
func (v *Verifier) load(ctx context.Context, rawURL string, now time.Time) (*x509.Certificate, error) {
	data, err := v.fetch(ctx, rawURL)
	if err != nil {
		return nil, fmt.Errorf("sns: descargar certificado: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("sns: el certificado no es PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("sns: certificado ilegible: %w", err)
	}
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return nil, errors.New("sns: certificado fuera de vigencia")
	}
	if _, ok := cert.PublicKey.(*rsa.PublicKey); !ok {
		return nil, errors.New("sns: el certificado no lleva una clave RSA")
	}
	return cert, nil
}

// ConfirmSubscription visita el SubscribeURL solo si es https y apunta a SNS: la URL
// viene del cuerpo de la peticion y, sin esta comprobacion, cualquiera podria hacer que
// el servicio visitara una direccion interna.
func (v *Verifier) ConfirmSubscription(ctx context.Context, subscribeURL string) error {
	u, err := url.Parse(subscribeURL)
	if err != nil || u.Scheme != "https" || !subscribeHostPattern.MatchString(u.Hostname()) || u.Port() != "" {
		return ErrSubscribeURL
	}
	if _, err := v.fetch(ctx, u.String()); err != nil {
		return fmt.Errorf("sns: confirmar suscripcion: %w", err)
	}
	return nil
}
