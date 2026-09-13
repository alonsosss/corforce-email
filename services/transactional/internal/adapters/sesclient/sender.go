// Package sesclient es el adaptador de Amazon SES v2 (SendEmail) tras el puerto
// ports.Sender. Las credenciales salen de la cadena estandar de AWS (rol IAM del host)
// salvo que SES_ACCESS_KEY_ID y SES_SECRET_ACCESS_KEY esten definidas.
package sesclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"
)

// sendTimeout acota cada llamada al proveedor.
const sendTimeout = 20 * time.Second

// api es la parte del cliente de SES que se usa; permite un doble en pruebas.
type api interface {
	SendEmail(ctx context.Context, in *sesv2.SendEmailInput, opts ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error)
}

type Sender struct {
	client    api
	configSet string
}

// Options del adaptador. Region y ConfigurationSet vienen de SES_REGION y
// SES_CONFIG_SET_TRANSACTIONAL.
type Options struct {
	Region           string
	ConfigurationSet string
	AccessKeyID      string
	SecretAccessKey  string
}

func New(ctx context.Context, opt Options) (*Sender, error) {
	if opt.Region == "" {
		return nil, errors.New("SES_REGION es obligatoria")
	}
	loaders := []func(*config.LoadOptions) error{config.WithRegion(opt.Region)}
	if opt.AccessKeyID != "" || opt.SecretAccessKey != "" {
		if opt.AccessKeyID == "" || opt.SecretAccessKey == "" {
			return nil, errors.New("SES_ACCESS_KEY_ID y SES_SECRET_ACCESS_KEY deben definirse juntas")
		}
		loaders = append(loaders, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(opt.AccessKeyID, opt.SecretAccessKey, "")))
	}
	cfg, err := config.LoadDefaultConfig(ctx, loaders...)
	if err != nil {
		return nil, fmt.Errorf("configurar AWS: %w", err)
	}
	// El SDK reintenta por su cuenta; aqui se desactiva para que la clasificacion y el
	// backoff los gobierne el worker con el limitador de tasa.
	cfg.RetryMaxAttempts = 1
	return &Sender{client: sesv2.NewFromConfig(cfg), configSet: opt.ConfigurationSet}, nil
}

// OptionsFromEnv lee las variables del entorno.
func OptionsFromEnv() Options {
	return Options{
		Region:           os.Getenv("SES_REGION"),
		ConfigurationSet: os.Getenv("SES_CONFIG_SET_TRANSACTIONAL"),
		AccessKeyID:      os.Getenv("SES_ACCESS_KEY_ID"),
		SecretAccessKey:  os.Getenv("SES_SECRET_ACCESS_KEY"),
	}
}

// WithConfigurationSet devuelve un emisor que comparte cliente y credenciales pero sale por
// otro configuration set: cada carril (transaccional, marketing) tiene el suyo, y SES
// separa por el sus eventos, sus metricas y el seguimiento de aperturas y clics.
func (s *Sender) WithConfigurationSet(name string) *Sender {
	return &Sender{client: s.client, configSet: name}
}

func (s *Sender) Send(ctx context.Context, email domain.OutgoingEmail) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	out, err := s.client.SendEmail(ctx, BuildInput(email, s.configSet))
	if err != nil {
		return "", Classify(err)
	}
	if out == nil || out.MessageId == nil {
		return "", &domain.SendError{Kind: domain.ErrorTransient, Code: "EmptyResponse", Message: "SES no devolvio MessageId"}
	}
	return *out.MessageId, nil
}

// BuildInput arma la peticion SendEmail: contenido simple, remitente con nombre, etiquetas
// tenant_id y message_id (por ellas se atribuye cada evento de SES) y las cabeceras de
// baja RFC 8058 cuando el mensaje es dable de baja.
func BuildInput(email domain.OutgoingEmail, configSet string) *sesv2.SendEmailInput {
	body := &types.Body{}
	if email.HTML != "" {
		body.Html = &types.Content{Data: aws.String(email.HTML), Charset: aws.String("UTF-8")}
	}
	if email.Text != "" {
		body.Text = &types.Content{Data: aws.String(email.Text), Charset: aws.String("UTF-8")}
	}
	msg := &types.Message{
		Subject: &types.Content{Data: aws.String(email.Subject), Charset: aws.String("UTF-8")},
		Body:    body,
	}
	for name, value := range email.Headers {
		msg.Headers = append(msg.Headers, types.MessageHeader{Name: aws.String(name), Value: aws.String(value)})
	}
	if email.UnsubscribeURL != "" {
		msg.Headers = append(msg.Headers,
			types.MessageHeader{Name: aws.String("List-Unsubscribe"), Value: aws.String("<" + email.UnsubscribeURL + ">")},
			types.MessageHeader{Name: aws.String("List-Unsubscribe-Post"), Value: aws.String("List-Unsubscribe=One-Click")},
		)
	}
	in := &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(email.From),
		Destination: &types.Destination{
			ToAddresses:  email.To,
			CcAddresses:  email.Cc,
			BccAddresses: email.Bcc,
		},
		ReplyToAddresses: email.ReplyTo,
		Content:          &types.EmailContent{Simple: msg},
		EmailTags: []types.MessageTag{
			{Name: aws.String("tenant_id"), Value: aws.String(email.TenantID.String())},
			{Name: aws.String("message_id"), Value: aws.String(email.MessageID.String())},
		},
	}
	for name, value := range email.Tags {
		if name == "tenant_id" || name == "message_id" {
			continue
		}
		in.EmailTags = append(in.EmailTags, types.MessageTag{Name: aws.String(name), Value: aws.String(value)})
	}
	if configSet != "" {
		in.ConfigurationSetName = aws.String(configSet)
	}
	return in
}

// Classify traduce el error del SDK a un domain.SendError. Throttling, cuota, pausa de
// cuenta, 5xx y fallos de red son transitorios (se reintentan); un rechazo del contenido
// o del remitente es permanente.
func Classify(err error) error {
	if err == nil {
		return nil
	}
	var se *domain.SendError
	if errors.As(err, &se) {
		return se
	}

	var (
		tooMany       *types.TooManyRequestsException
		limit         *types.LimitExceededException
		internal      *types.InternalServiceErrorException
		paused        *types.SendingPausedException
		suspended     *types.AccountSuspendedException
		badRequest    *types.BadRequestException
		rejected      *types.MessageRejected
		mailFrom      *types.MailFromDomainNotVerifiedException
		notFound      *types.NotFoundException
		responseError *awshttp.ResponseError
		apiErr        smithy.APIError
	)
	switch {
	case errors.As(err, &tooMany), errors.As(err, &limit), errors.As(err, &internal),
		errors.As(err, &paused), errors.As(err, &suspended):
		return &domain.SendError{Kind: domain.ErrorTransient, Code: code(err), Message: message(err)}
	case errors.As(err, &badRequest), errors.As(err, &rejected), errors.As(err, &mailFrom), errors.As(err, &notFound):
		return &domain.SendError{Kind: domain.ErrorPermanent, Code: code(err), Message: message(err)}
	}
	if errors.As(err, &responseError) {
		status := responseError.HTTPStatusCode()
		kind := domain.ErrorTransient
		if status >= 400 && status < 500 && status != http.StatusTooManyRequests && status != http.StatusRequestTimeout {
			kind = domain.ErrorPermanent
		}
		return &domain.SendError{Kind: kind, Code: code(err), Message: message(err)}
	}
	if errors.As(err, &apiErr) {
		// Un error de API sin transporte conocido: se reintenta salvo que el propio SDK
		// diga que es del cliente.
		kind := domain.ErrorTransient
		if apiErr.ErrorFault() == smithy.FaultClient {
			kind = domain.ErrorPermanent
		}
		return &domain.SendError{Kind: kind, Code: apiErr.ErrorCode(), Message: apiErr.ErrorMessage()}
	}
	// Red, DNS, timeout: transitorio.
	return &domain.SendError{Kind: domain.ErrorTransient, Code: "Transport", Message: err.Error()}
}

func code(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() != "" {
		return apiErr.ErrorCode()
	}
	return "Unknown"
}

func message(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorMessage() != "" {
		return apiErr.ErrorMessage()
	}
	return err.Error()
}
