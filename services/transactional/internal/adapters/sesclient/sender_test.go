package sesclient

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/google/uuid"
)

// wrapped reproduce la cadena real del SDK: OperationError > ResponseError > excepcion.
func wrapped(status int, inner error) error {
	return &smithy.OperationError{
		ServiceID:     "SESv2",
		OperationName: "SendEmail",
		Err: &awshttp.ResponseError{
			ResponseError: &smithyhttp.ResponseError{
				Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
				Err:      inner,
			},
			RequestID: "req-1",
		},
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		err  error
		kind domain.ErrorKind
		code string
	}{
		{"throttling", wrapped(429, &types.TooManyRequestsException{Message: aws.String("Maximum sending rate exceeded.")}), domain.ErrorTransient, "TooManyRequestsException"},
		{"cuota", wrapped(400, &types.LimitExceededException{}), domain.ErrorTransient, "LimitExceededException"},
		{"5xx interno", wrapped(500, &types.InternalServiceErrorException{}), domain.ErrorTransient, "InternalServiceErrorException"},
		{"envio pausado", wrapped(400, &types.SendingPausedException{}), domain.ErrorTransient, "SendingPausedException"},
		{"mensaje rechazado", wrapped(400, &types.MessageRejected{Message: aws.String("Email address is not verified.")}), domain.ErrorPermanent, "MessageRejected"},
		{"peticion invalida", wrapped(400, &types.BadRequestException{}), domain.ErrorPermanent, "BadRequestException"},
		{"mail from no verificado", wrapped(400, &types.MailFromDomainNotVerifiedException{}), domain.ErrorPermanent, "MailFromDomainNotVerifiedException"},
		{"503 sin tipo", wrapped(503, errors.New("service unavailable")), domain.ErrorTransient, "Unknown"},
		{"400 sin tipo", wrapped(400, &smithy.GenericAPIError{Code: "InvalidParameterValue", Message: "bad", Fault: smithy.FaultClient}), domain.ErrorPermanent, "InvalidParameterValue"},
		{"408 sin tipo", wrapped(408, errors.New("timeout")), domain.ErrorTransient, "Unknown"},
		{"api servidor", &smithy.GenericAPIError{Code: "Throttling", Fault: smithy.FaultServer}, domain.ErrorTransient, "Throttling"},
		{"api cliente", &smithy.GenericAPIError{Code: "ValidationError", Fault: smithy.FaultClient}, domain.ErrorPermanent, "ValidationError"},
		{"red", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, domain.ErrorTransient, "Transport"},
		{"timeout", context.DeadlineExceeded, domain.ErrorTransient, "Transport"},
	}
	for _, tc := range cases {
		var se *domain.SendError
		if !errors.As(Classify(tc.err), &se) {
			t.Errorf("%s: Classify no devolvio un SendError", tc.name)
			continue
		}
		if se.Kind != tc.kind || se.Code != tc.code {
			t.Errorf("%s: kind=%s code=%s, se esperaba %s/%s", tc.name, se.Kind, se.Code, tc.kind, tc.code)
		}
	}
	if Classify(nil) != nil {
		t.Error("Classify(nil) debe ser nil")
	}
}

func outgoing() domain.OutgoingEmail {
	return domain.OutgoingEmail{
		MessageID: uuid.New(),
		TenantID:  uuid.New(),
		From:      `"Tienda" <no-reply@example.com>`,
		ReplyTo:   []string{"soporte@example.com"},
		To:        []string{"ana@example.com"},
		Subject:   "Pedido confirmado",
		HTML:      "<p>Gracias</p>",
		Text:      "Gracias",
		Headers:   map[string]string{"X-Order-Id": "A-100"},
		Tags:      map[string]string{"flow": "order", "tenant_id": "intento-de-suplantar"},
	}
}

func headerValue(in *sesv2.SendEmailInput, name string) (string, bool) {
	for _, h := range in.Content.Simple.Headers {
		if aws.ToString(h.Name) == name {
			return aws.ToString(h.Value), true
		}
	}
	return "", false
}

func tagValues(in *sesv2.SendEmailInput, name string) []string {
	var out []string
	for _, t := range in.EmailTags {
		if aws.ToString(t.Name) == name {
			out = append(out, aws.ToString(t.Value))
		}
	}
	return out
}

func TestBuildInput(t *testing.T) {
	email := outgoing()
	email.UnsubscribeURL = "https://app.example.com/api/v1/public/transactional/unsubscribe?t=1&m=2&e=a&sig=f"
	in := BuildInput(email, "cfm-transactional")

	if aws.ToString(in.ConfigurationSetName) != "cfm-transactional" {
		t.Errorf("configuration set = %q", aws.ToString(in.ConfigurationSetName))
	}
	if aws.ToString(in.FromEmailAddress) != email.From || in.ReplyToAddresses[0] != "soporte@example.com" {
		t.Errorf("remitente o reply-to incorrectos")
	}
	if aws.ToString(in.Content.Simple.Body.Html.Data) != "<p>Gracias</p>" || aws.ToString(in.Content.Simple.Body.Text.Data) != "Gracias" {
		t.Errorf("cuerpo incorrecto")
	}
	if v, ok := headerValue(in, "List-Unsubscribe"); !ok || v != "<"+email.UnsubscribeURL+">" {
		t.Errorf("List-Unsubscribe = %q", v)
	}
	if v, _ := headerValue(in, "List-Unsubscribe-Post"); v != "List-Unsubscribe=One-Click" {
		t.Errorf("List-Unsubscribe-Post = %q", v)
	}
	if v, _ := headerValue(in, "X-Order-Id"); v != "A-100" {
		t.Errorf("cabecera propia perdida")
	}
	if got := tagValues(in, "tenant_id"); len(got) != 1 || got[0] != email.TenantID.String() {
		t.Errorf("tenant_id debe ser el del mensaje y unico: %v", got)
	}
	if got := tagValues(in, "message_id"); len(got) != 1 || got[0] != email.MessageID.String() {
		t.Errorf("message_id = %v", got)
	}
	if got := tagValues(in, "flow"); len(got) != 1 || got[0] != "order" {
		t.Errorf("etiqueta del cliente perdida: %v", got)
	}
}

func TestBuildInputWithoutUnsubscribe(t *testing.T) {
	email := outgoing()
	email.Text = ""
	in := BuildInput(email, "")
	if _, ok := headerValue(in, "List-Unsubscribe"); ok {
		t.Error("un mensaje transaccional puro no lleva List-Unsubscribe")
	}
	if in.ConfigurationSetName != nil {
		t.Error("sin configuration set no se envia el campo")
	}
	if in.Content.Simple.Body.Text != nil {
		t.Error("sin texto no se envia la parte de texto")
	}
}

type fakeAPI struct {
	in  *sesv2.SendEmailInput
	out *sesv2.SendEmailOutput
	err error
}

func (f *fakeAPI) SendEmail(_ context.Context, in *sesv2.SendEmailInput, _ ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	f.in = in
	return f.out, f.err
}

func TestSend(t *testing.T) {
	api := &fakeAPI{out: &sesv2.SendEmailOutput{MessageId: aws.String("0100018f-abc")}}
	s := &Sender{client: api, configSet: "cfm-transactional"}
	id, err := s.Send(context.Background(), outgoing())
	if err != nil || id != "0100018f-abc" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if aws.ToString(api.in.ConfigurationSetName) != "cfm-transactional" {
		t.Error("el configuration set no llego a SES")
	}

	api.err = wrapped(400, &types.MessageRejected{})
	_, err = s.Send(context.Background(), outgoing())
	var se *domain.SendError
	if !errors.As(err, &se) || se.Kind != domain.ErrorPermanent {
		t.Fatalf("el error debe llegar clasificado: %v", err)
	}

	api.err, api.out = nil, &sesv2.SendEmailOutput{}
	if _, err := s.Send(context.Background(), outgoing()); !errors.As(err, &se) || se.Kind != domain.ErrorTransient {
		t.Fatalf("una respuesta sin MessageId es transitoria: %v", err)
	}
}

func TestWithConfigurationSetSeparatesLanes(t *testing.T) {
	api := &fakeAPI{out: &sesv2.SendEmailOutput{MessageId: aws.String("0100018f-abc")}}
	transactional := &Sender{client: api, configSet: "cfm-transactional"}
	marketing := transactional.WithConfigurationSet("cfm-marketing")

	if _, err := marketing.Send(context.Background(), outgoing()); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(api.in.ConfigurationSetName) != "cfm-marketing" {
		t.Fatalf("el carril de marketing sale por su set: %q", aws.ToString(api.in.ConfigurationSetName))
	}
	if _, err := transactional.Send(context.Background(), outgoing()); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(api.in.ConfigurationSetName) != "cfm-transactional" {
		t.Fatalf("el emisor transaccional no cambia de set: %q", aws.ToString(api.in.ConfigurationSetName))
	}
}

func TestNewRequiresRegionAndPairedKeys(t *testing.T) {
	if _, err := New(context.Background(), Options{}); err == nil {
		t.Error("sin region debe fallar")
	}
	if _, err := New(context.Background(), Options{Region: "us-east-1", AccessKeyID: "AKIA"}); err == nil {
		t.Error("una clave sin su secreto debe fallar")
	}
	if _, err := New(context.Background(), Options{Region: "us-east-1", AccessKeyID: "AKIA", SecretAccessKey: "s"}); err != nil {
		t.Errorf("claves estaticas completas: %v", err)
	}
}
