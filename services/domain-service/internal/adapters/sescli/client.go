// Package sescli es el adaptador de las identidades de Amazon SES v2 tras el puerto
// ports.SESIdentityClient. Usa credenciales propias (SES_IDENTITIES_ACCESS_KEY_ID y
// SES_IDENTITIES_SECRET_ACCESS_KEY, del usuario core-force-mail-ses-identidades de
// ops/aws/setup-iam.sh), distintas de las de envio de transactional: una clave de envio filtrada
// no cambia las claves DKIM de ninguna empresa, y esta no envia correo.
package sescli

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/ports"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/google/uuid"
)

// callTimeout acota cada llamada a SES: el barrido las hace con el cerrojo del dominio tomado.
const callTimeout = 15 * time.Second

// api es la parte del cliente de SES que se usa; permite un doble en pruebas.
type api interface {
	GetEmailIdentity(ctx context.Context, in *sesv2.GetEmailIdentityInput, opts ...func(*sesv2.Options)) (*sesv2.GetEmailIdentityOutput, error)
	CreateEmailIdentity(ctx context.Context, in *sesv2.CreateEmailIdentityInput, opts ...func(*sesv2.Options)) (*sesv2.CreateEmailIdentityOutput, error)
	PutEmailIdentityDkimSigningAttributes(ctx context.Context, in *sesv2.PutEmailIdentityDkimSigningAttributesInput, opts ...func(*sesv2.Options)) (*sesv2.PutEmailIdentityDkimSigningAttributesOutput, error)
	PutEmailIdentityMailFromAttributes(ctx context.Context, in *sesv2.PutEmailIdentityMailFromAttributesInput, opts ...func(*sesv2.Options)) (*sesv2.PutEmailIdentityMailFromAttributesOutput, error)
	PutEmailIdentityConfigurationSetAttributes(ctx context.Context, in *sesv2.PutEmailIdentityConfigurationSetAttributesInput, opts ...func(*sesv2.Options)) (*sesv2.PutEmailIdentityConfigurationSetAttributesOutput, error)
	DeleteEmailIdentity(ctx context.Context, in *sesv2.DeleteEmailIdentityInput, opts ...func(*sesv2.Options)) (*sesv2.DeleteEmailIdentityOutput, error)
	TagResource(ctx context.Context, in *sesv2.TagResourceInput, opts ...func(*sesv2.Options)) (*sesv2.TagResourceOutput, error)
}

// accountAPI da la cuenta de las credenciales, que forma el ARN de la identidad al etiquetarla.
type accountAPI interface {
	GetCallerIdentity(ctx context.Context, in *sts.GetCallerIdentityInput, opts ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// Client implementa ports.SESIdentityClient.
type Client struct {
	api     api
	sts     accountAPI
	region  string
	mu      sync.Mutex
	account string
}

var _ ports.SESIdentityClient = (*Client)(nil)

// Options del adaptador. Region viene de SES_REGION; ConfigurationSet de SES_CONFIG_SET_TRANSACTIONAL.
type Options struct {
	Region           string
	ConfigurationSet string
	AccessKeyID      string
	SecretAccessKey  string
}

// OptionsFromEnv lee las variables del entorno.
func OptionsFromEnv() Options {
	return Options{
		Region:           strings.TrimSpace(os.Getenv("SES_REGION")),
		ConfigurationSet: strings.TrimSpace(os.Getenv("SES_CONFIG_SET_TRANSACTIONAL")),
		AccessKeyID:      strings.TrimSpace(os.Getenv("SES_IDENTITIES_ACCESS_KEY_ID")),
		SecretAccessKey:  strings.TrimSpace(os.Getenv("SES_IDENTITIES_SECRET_ACCESS_KEY")),
	}
}

// Enabled dice si hay credenciales: sin ninguna de las dos la integracion queda desactivada.
func (o Options) Enabled() bool { return o.AccessKeyID != "" || o.SecretAccessKey != "" }

// Validate exige, con la integracion activa, las dos claves, una region valida y el conjunto.
func (o Options) Validate() error {
	var problems []string
	if o.AccessKeyID == "" || o.SecretAccessKey == "" {
		problems = append(problems, "SES_IDENTITIES_ACCESS_KEY_ID y SES_IDENTITIES_SECRET_ACCESS_KEY deben definirse juntas")
	}
	if !domain.ValidSESRegion(o.Region) {
		problems = append(problems, fmt.Sprintf("SES_REGION %q no es una region de AWS", o.Region))
	}
	if o.ConfigurationSet == "" {
		problems = append(problems, "falta SES_CONFIG_SET_TRANSACTIONAL, el conjunto por defecto de las identidades")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// New arma el cliente con credenciales estaticas; nunca cae a la cadena del host, que en el servidor
// propio no existe y en EC2 seria el rol de la instancia, con otros permisos.
func New(ctx context.Context, opt Options) (*Client, error) {
	if err := opt.Validate(); err != nil {
		return nil, err
	}
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(opt.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(opt.AccessKeyID, opt.SecretAccessKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("configurar AWS: %w", err)
	}
	return &Client{api: sesv2.NewFromConfig(cfg), sts: sts.NewFromConfig(cfg), region: opt.Region}, nil
}

// TagIdentity etiqueta la identidad con la empresa. La cuenta se pregunta una vez a STS (no necesita
// permiso) y se recuerda.
func (c *Client) TagIdentity(ctx context.Context, tenantID uuid.UUID, name string) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	account, err := c.accountID(ctx)
	if err != nil {
		return err
	}
	arn := fmt.Sprintf("arn:aws:ses:%s:%s:identity/%s", c.region, account, name)
	_, err = c.api.TagResource(ctx, &sesv2.TagResourceInput{
		ResourceArn: aws.String(arn),
		Tags:        []types.Tag{{Key: aws.String(domain.SESTenantTag), Value: aws.String(tenantID.String())}},
	})
	if err != nil {
		return classify("TagResource", err)
	}
	return nil
}

func (c *Client) accountID(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.account != "" {
		return c.account, nil
	}
	out, err := c.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("STS GetCallerIdentity: %w", err)
	}
	account := aws.ToString(out.Account)
	if len(account) != 12 {
		return "", fmt.Errorf("STS devolvio una cuenta no valida: %q", account)
	}
	c.account = account
	return account, nil
}

func (c *Client) GetIdentity(ctx context.Context, name string) (domain.SESIdentityObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	out, err := c.api.GetEmailIdentity(ctx, &sesv2.GetEmailIdentityInput{EmailIdentity: aws.String(name)})
	if err != nil {
		return domain.SESIdentityObservation{}, classify("GetEmailIdentity", err)
	}
	return Observation(out), nil
}

// Observation traduce la respuesta de GetEmailIdentity.
func Observation(out *sesv2.GetEmailIdentityOutput) domain.SESIdentityObservation {
	obs := domain.SESIdentityObservation{
		VerifiedForSending: out.VerifiedForSendingStatus,
		ConfigurationSet:   aws.ToString(out.ConfigurationSetName),
	}
	if a := out.DkimAttributes; a != nil {
		obs.DKIMOrigin = string(a.SigningAttributesOrigin)
		obs.DKIMSelectors = append([]string(nil), a.Tokens...)
		obs.DKIMStatus = domain.ParseSESCheckStatus(string(a.Status))
	}
	if m := out.MailFromAttributes; m != nil {
		obs.MailFromDomain = strings.ToLower(aws.ToString(m.MailFromDomain))
		obs.MailFromStatus = domain.ParseSESCheckStatus(string(m.MailFromDomainStatus))
		obs.BehaviorOnMXFailure = string(m.BehaviorOnMxFailure)
	}
	for _, t := range out.Tags {
		if aws.ToString(t.Key) == domain.SESTenantTag {
			obs.TenantTag = aws.ToString(t.Value)
		}
	}
	return obs
}

func (c *Client) CreateIdentity(ctx context.Context, tenantID uuid.UUID, name string, key ports.DKIMKey, configSet string) error {
	in, err := CreateInput(tenantID, name, key, configSet)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	if _, err := c.api.CreateEmailIdentity(ctx, in); err != nil {
		return classify("CreateEmailIdentity", err)
	}
	return nil
}

// CreateInput arma la peticion de alta: BYODKIM con la clave y el selector de domain-service, el
// conjunto por defecto y la etiqueta de la empresa.
func CreateInput(tenantID uuid.UUID, name string, key ports.DKIMKey, configSet string) (*sesv2.CreateEmailIdentityInput, error) {
	attrs, err := signingAttributes(key)
	if err != nil {
		return nil, err
	}
	return &sesv2.CreateEmailIdentityInput{
		EmailIdentity:         aws.String(name),
		ConfigurationSetName:  aws.String(configSet),
		DkimSigningAttributes: attrs,
		Tags:                  []types.Tag{{Key: aws.String(domain.SESTenantTag), Value: aws.String(tenantID.String())}},
	}, nil
}

func (c *Client) SetDKIMKey(ctx context.Context, name string, key ports.DKIMKey) error {
	in, err := DKIMInput(name, key)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	if _, err := c.api.PutEmailIdentityDkimSigningAttributes(ctx, in); err != nil {
		return classify("PutEmailIdentityDkimSigningAttributes", err)
	}
	return nil
}

// DKIMInput arma el cambio de clave: origen EXTERNAL con el selector y la clave nuevos.
func DKIMInput(name string, key ports.DKIMKey) (*sesv2.PutEmailIdentityDkimSigningAttributesInput, error) {
	attrs, err := signingAttributes(key)
	if err != nil {
		return nil, err
	}
	return &sesv2.PutEmailIdentityDkimSigningAttributesInput{
		EmailIdentity:           aws.String(name),
		SigningAttributesOrigin: types.DkimSigningAttributesOriginExternal,
		SigningAttributes:       attrs,
	}, nil
}

func (c *Client) SetMailFrom(ctx context.Context, name, mailFromDomain string) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, err := c.api.PutEmailIdentityMailFromAttributes(ctx, &sesv2.PutEmailIdentityMailFromAttributesInput{
		EmailIdentity:       aws.String(name),
		MailFromDomain:      aws.String(mailFromDomain),
		BehaviorOnMxFailure: types.BehaviorOnMxFailure(domain.SESBehaviorOnMXFailure),
	})
	if err != nil {
		return classify("PutEmailIdentityMailFromAttributes", err)
	}
	return nil
}

func (c *Client) SetConfigurationSet(ctx context.Context, name, configSet string) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, err := c.api.PutEmailIdentityConfigurationSetAttributes(ctx, &sesv2.PutEmailIdentityConfigurationSetAttributesInput{
		EmailIdentity:        aws.String(name),
		ConfigurationSetName: aws.String(configSet),
	})
	if err != nil {
		return classify("PutEmailIdentityConfigurationSetAttributes", err)
	}
	return nil
}

func (c *Client) DeleteIdentity(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, err := c.api.DeleteEmailIdentity(ctx, &sesv2.DeleteEmailIdentityInput{EmailIdentity: aws.String(name)})
	if err = classify("DeleteEmailIdentity", err); err != nil && !errors.Is(err, domain.ErrSESIdentityNotFound) {
		return err
	}
	return nil
}

// signingAttributes da la clave como la pide SES en BYODKIM: la DER PKCS#1 en base64, sin las
// cabeceras ni los saltos del PEM que custodia domain-service.
func signingAttributes(key ports.DKIMKey) (*types.DkimSigningAttributes, error) {
	der, err := privateKeyDER(key.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	if key.Selector == "" {
		return nil, errors.New("clave DKIM sin selector")
	}
	return &types.DkimSigningAttributes{
		DomainSigningSelector:   aws.String(key.Selector),
		DomainSigningPrivateKey: aws.String(base64.StdEncoding.EncodeToString(der)),
	}, nil
}

// privateKeyDER extrae la DER de una clave RSA PKCS#1 en PEM. El error nunca repite la clave.
func privateKeyDER(privatePEM string) ([]byte, error) {
	block, rest := pem.Decode([]byte(privatePEM))
	if block == nil || block.Type != "RSA PRIVATE KEY" || len(strings.TrimSpace(string(rest))) > 0 {
		return nil, errors.New("la clave DKIM no es un unico PEM RSA PKCS#1")
	}
	return block.Bytes, nil
}

// classify traduce los errores de SES a los de domain. El mensaje del SDK no lleva la clave: SES no
// la repite en sus respuestas.
func classify(op string, err error) error {
	if err == nil {
		return nil
	}
	var notFound *types.NotFoundException
	if errors.As(err, &notFound) {
		return fmt.Errorf("%s: %w", op, domain.ErrSESIdentityNotFound)
	}
	var exists *types.AlreadyExistsException
	if errors.As(err, &exists) {
		return fmt.Errorf("%s: %w", op, domain.ErrSESIdentityExists)
	}
	return fmt.Errorf("%s: %w", op, err)
}
