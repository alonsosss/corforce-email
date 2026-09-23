package sescli

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/ports"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/google/uuid"
)

func testKey(t *testing.T) (*rsa.PrivateKey, ports.DKIMKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, domain.DKIMKeyBits)
	if err != nil {
		t.Fatal(err)
	}
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)}
	return priv, ports.DKIMKey{Selector: "cfm202609", PrivateKeyPEM: string(pem.EncodeToMemory(block))}
}

// decodeKey hace lo que hace SES con DomainSigningPrivateKey: base64 de la DER PKCS#1.
func decodeKey(t *testing.T, attrs *types.DkimSigningAttributes) *rsa.PrivateKey {
	t.Helper()
	raw := aws.ToString(attrs.DomainSigningPrivateKey)
	if strings.Contains(raw, "BEGIN") || strings.ContainsAny(raw, "\n\r ") {
		t.Fatalf("la clave va sin cabeceras ni saltos del PEM: %.40q", raw)
	}
	der, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("no es base64: %v", err)
	}
	key, err := x509.ParsePKCS1PrivateKey(der)
	if err != nil {
		t.Fatalf("no es una clave RSA PKCS#1: %v", err)
	}
	return key
}

func TestElAltaLlevaLaClaveDKIMEnElFormatoQuePideSES(t *testing.T) {
	priv, key := testKey(t)
	tenant := uuid.New()
	in, err := CreateInput(tenant, "envio.com", key, "cfm-transactional")
	if err != nil {
		t.Fatalf("CreateInput: %v", err)
	}
	if aws.ToString(in.EmailIdentity) != "envio.com" || aws.ToString(in.ConfigurationSetName) != "cfm-transactional" {
		t.Errorf("identidad %q conjunto %q", aws.ToString(in.EmailIdentity), aws.ToString(in.ConfigurationSetName))
	}
	if aws.ToString(in.DkimSigningAttributes.DomainSigningSelector) != "cfm202609" {
		t.Errorf("selector = %q", aws.ToString(in.DkimSigningAttributes.DomainSigningSelector))
	}
	if got := decodeKey(t, in.DkimSigningAttributes); !got.Equal(priv) {
		t.Fatal("la clave que recibe SES no es la que custodia domain-service")
	}
	if len(in.Tags) != 1 || aws.ToString(in.Tags[0].Key) != domain.SESTenantTag || aws.ToString(in.Tags[0].Value) != tenant.String() {
		t.Errorf("etiqueta de la empresa: %+v", in.Tags)
	}
}

func TestElCambioDeClaveEsBYODKIMConElSelectorNuevo(t *testing.T) {
	priv, key := testKey(t)
	key.Selector = "cfm20260923"
	in, err := DKIMInput("envio.com", key)
	if err != nil {
		t.Fatalf("DKIMInput: %v", err)
	}
	if in.SigningAttributesOrigin != types.DkimSigningAttributesOriginExternal {
		t.Errorf("origen = %s", in.SigningAttributesOrigin)
	}
	if aws.ToString(in.SigningAttributes.DomainSigningSelector) != "cfm20260923" || !decodeKey(t, in.SigningAttributes).Equal(priv) {
		t.Fatal("selector o clave nuevos")
	}
}

func TestUnaClaveQueNoEsPEMRSASeRechazaSinRepetirla(t *testing.T) {
	_, key := testKey(t)
	for name, pemText := range map[string]string{
		"vacia":     "",
		"otro tipo": strings.Replace(key.PrivateKeyPEM, "RSA PRIVATE KEY", "PRIVATE KEY", 2),
		"dos PEM":   key.PrivateKeyPEM + key.PrivateKeyPEM,
	} {
		_, err := CreateInput(uuid.New(), "envio.com", ports.DKIMKey{Selector: "s", PrivateKeyPEM: pemText}, "c")
		if err == nil {
			t.Errorf("%s: se esperaba error", name)
			continue
		}
		if len(pemText) > 60 && strings.Contains(err.Error(), pemText[40:60]) {
			t.Errorf("%s: el error repite la clave", name)
		}
	}
	if _, err := DKIMInput("envio.com", ports.DKIMKey{PrivateKeyPEM: key.PrivateKeyPEM}); err == nil {
		t.Error("sin selector se rechaza")
	}
}

func TestLaObservacionTraduceLaRespuestaDeSES(t *testing.T) {
	obs := Observation(&sesv2.GetEmailIdentityOutput{
		VerifiedForSendingStatus: true,
		ConfigurationSetName:     aws.String("cfm-transactional"),
		DkimAttributes: &types.DkimAttributes{
			SigningAttributesOrigin: types.DkimSigningAttributesOriginExternal,
			Status:                  types.DkimStatusSuccess, Tokens: []string{"cfm202609"},
		},
		MailFromAttributes: &types.MailFromAttributes{
			MailFromDomain: aws.String("Bounce.Envio.com"), MailFromDomainStatus: types.MailFromDomainStatusPending,
			BehaviorOnMxFailure: types.BehaviorOnMxFailureUseDefaultValue,
		},
		Tags: []types.Tag{{Key: aws.String("otra"), Value: aws.String("x")}, {Key: aws.String(domain.SESTenantTag), Value: aws.String("t1")}},
	})
	if !obs.VerifiedForSending || !obs.SignsWith("cfm202609") || obs.DKIMStatus != domain.SESCheckSuccess {
		t.Errorf("DKIM: %+v", obs)
	}
	if obs.MailFromDomain != "bounce.envio.com" || obs.MailFromStatus != domain.SESCheckPending || obs.BehaviorOnMXFailure != domain.SESBehaviorOnMXFailure {
		t.Errorf("MAIL FROM: %+v", obs)
	}
	if obs.ConfigurationSet != "cfm-transactional" || obs.TenantTag != "t1" {
		t.Errorf("conjunto o etiqueta: %+v", obs)
	}
	if empty := Observation(&sesv2.GetEmailIdentityOutput{}); empty.VerifiedForSending || empty.SignsWith("") || empty.TenantTag != "" {
		t.Errorf("una respuesta sin atributos: %+v", empty)
	}
}

// fakeAPI responde con el error que se le fije y anota las peticiones.
type fakeAPI struct {
	err     error
	deleted []string
	mailIn  *sesv2.PutEmailIdentityMailFromAttributesInput
}

func (f *fakeAPI) GetEmailIdentity(context.Context, *sesv2.GetEmailIdentityInput, ...func(*sesv2.Options)) (*sesv2.GetEmailIdentityOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &sesv2.GetEmailIdentityOutput{}, nil
}
func (f *fakeAPI) CreateEmailIdentity(context.Context, *sesv2.CreateEmailIdentityInput, ...func(*sesv2.Options)) (*sesv2.CreateEmailIdentityOutput, error) {
	return &sesv2.CreateEmailIdentityOutput{}, f.err
}
func (f *fakeAPI) PutEmailIdentityDkimSigningAttributes(context.Context, *sesv2.PutEmailIdentityDkimSigningAttributesInput, ...func(*sesv2.Options)) (*sesv2.PutEmailIdentityDkimSigningAttributesOutput, error) {
	return &sesv2.PutEmailIdentityDkimSigningAttributesOutput{}, f.err
}
func (f *fakeAPI) PutEmailIdentityMailFromAttributes(_ context.Context, in *sesv2.PutEmailIdentityMailFromAttributesInput, _ ...func(*sesv2.Options)) (*sesv2.PutEmailIdentityMailFromAttributesOutput, error) {
	f.mailIn = in
	return &sesv2.PutEmailIdentityMailFromAttributesOutput{}, f.err
}
func (f *fakeAPI) PutEmailIdentityConfigurationSetAttributes(context.Context, *sesv2.PutEmailIdentityConfigurationSetAttributesInput, ...func(*sesv2.Options)) (*sesv2.PutEmailIdentityConfigurationSetAttributesOutput, error) {
	return &sesv2.PutEmailIdentityConfigurationSetAttributesOutput{}, f.err
}
func (f *fakeAPI) DeleteEmailIdentity(_ context.Context, in *sesv2.DeleteEmailIdentityInput, _ ...func(*sesv2.Options)) (*sesv2.DeleteEmailIdentityOutput, error) {
	f.deleted = append(f.deleted, aws.ToString(in.EmailIdentity))
	return &sesv2.DeleteEmailIdentityOutput{}, f.err
}

func TestLosErroresDeSESSeTraducenALosDelDominio(t *testing.T) {
	ctx := context.Background()
	_, key := testKey(t)

	notFound := &fakeAPI{err: &types.NotFoundException{Message: aws.String("no existe")}}
	c := &Client{api: notFound}
	if _, err := c.GetIdentity(ctx, "envio.com"); !errors.Is(err, domain.ErrSESIdentityNotFound) {
		t.Errorf("GetIdentity sin identidad: %v", err)
	}
	if err := c.DeleteIdentity(ctx, "envio.com"); err != nil || len(notFound.deleted) != 1 {
		t.Errorf("borrar lo que SES ya no tiene no falla: %v", err)
	}

	exists := &Client{api: &fakeAPI{err: &types.AlreadyExistsException{Message: aws.String("ya existe")}}}
	if err := exists.CreateIdentity(ctx, uuid.New(), "envio.com", key, "c"); !errors.Is(err, domain.ErrSESIdentityExists) {
		t.Errorf("CreateIdentity repetido: %v", err)
	}

	throttled := &Client{api: &fakeAPI{err: &types.TooManyRequestsException{Message: aws.String("despacio")}}}
	if err := throttled.DeleteIdentity(ctx, "envio.com"); err == nil || errors.Is(err, domain.ErrSESIdentityNotFound) {
		t.Errorf("un limite de SES es un fallo que se reintenta: %v", err)
	}

	ok := &fakeAPI{}
	if err := (&Client{api: ok}).SetMailFrom(ctx, "envio.com", "bounce.envio.com"); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(ok.mailIn.MailFromDomain) != "bounce.envio.com" || ok.mailIn.BehaviorOnMxFailure != types.BehaviorOnMxFailureUseDefaultValue {
		t.Errorf("MAIL FROM: %+v", ok.mailIn)
	}
}

func TestSinCredencialesLaIntegracionQuedaDesactivadaYConUnaSolaNoArranca(t *testing.T) {
	if (Options{Region: "us-east-1", ConfigurationSet: "c"}).Enabled() {
		t.Fatal("sin claves esta desactivada")
	}
	cases := map[string]Options{
		"solo una clave":     {AccessKeyID: "AKIA", Region: "us-east-1", ConfigurationSet: "c"},
		"region invalida":    {AccessKeyID: "AKIA", SecretAccessKey: "s", Region: "us-east-1\nx", ConfigurationSet: "c"},
		"sin conjunto":       {AccessKeyID: "AKIA", SecretAccessKey: "s", Region: "us-east-1"},
		"sin region":         {AccessKeyID: "AKIA", SecretAccessKey: "s", ConfigurationSet: "c"},
		"region con comodin": {AccessKeyID: "AKIA", SecretAccessKey: "s", Region: "us-*-1", ConfigurationSet: "c"},
	}
	for name, opt := range cases {
		if !opt.Enabled() {
			t.Errorf("%s: con alguna clave cuenta como activa", name)
		}
		if err := opt.Validate(); err == nil {
			t.Errorf("%s: se esperaba error", name)
		} else if strings.Contains(err.Error(), "AKIA") || strings.Contains(err.Error(), `"s"`) {
			t.Errorf("%s: el error repite una clave: %v", name, err)
		}
	}
	good := Options{AccessKeyID: "AKIA", SecretAccessKey: "s", Region: "eu-central-1", ConfigurationSet: "cfm-transactional"}
	if err := good.Validate(); err != nil {
		t.Fatalf("configuracion completa: %v", err)
	}
	if _, err := New(context.Background(), good); err != nil {
		t.Fatalf("New: %v", err)
	}
}

func TestOptionsFromEnvLeeLasVariablesPropias(t *testing.T) {
	t.Setenv("SES_REGION", " us-east-1 ")
	t.Setenv("SES_CONFIG_SET_TRANSACTIONAL", "cfm-transactional")
	t.Setenv("SES_IDENTITIES_ACCESS_KEY_ID", "id")
	t.Setenv("SES_IDENTITIES_SECRET_ACCESS_KEY", "secreto")
	t.Setenv("SES_ACCESS_KEY_ID", "de-envio")
	opt := OptionsFromEnv()
	if opt.Region != "us-east-1" || opt.ConfigurationSet != "cfm-transactional" || opt.AccessKeyID != "id" || opt.SecretAccessKey != "secreto" {
		t.Fatalf("opciones: region %q conjunto %q", opt.Region, opt.ConfigurationSet)
	}
}
