package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/anthropic"
	"github.com/alonsosss/corforce-email/services/webmail/internal/app"
	"github.com/alonsosss/corforce-email/services/webmail/internal/ports"
	"go.uber.org/zap"
)

// Asistente del webmail (docs/adr/0015-asistente-del-webmail-con-claude.md). La clave del proveedor
// (ANTHROPIC_API_KEY) llega solo del almacen de secretos y es opcional: sin ella el webmail arranca y
// el asistente queda no disponible. Ademas cada empresa lo activa desde el panel (mail-directory).
const (
	defaultAssistantAPIURL      = "https://api.anthropic.com"
	defaultAssistantModel       = "claude-haiku-4-5"
	defaultAssistantMaxTokens   = 1024
	maxAssistantMaxTokens       = 8192
	defaultAssistantTimeout     = 30 * time.Second
	defaultAssistantAttempts    = 3
	maxAssistantAttempts        = 5
	defaultAssistantInputChars  = 24000
	maxAssistantInputChars      = 200000
	defaultAssistantThread      = 20
	maxAssistantThread          = 50
	defaultAssistantInstruction = 500
	maxAssistantInstruction     = 2000
	defaultAssistantMailboxDay  = 50
	defaultAssistantTenantDay   = 500
	maxAssistantDaily           = 100000
	defaultAssistantSettingsTTL = 30 * time.Second
)

type assistantSettings struct {
	provider *anthropic.Config
	config   app.AssistantConfig
}

// loadAssistantSettings lee la configuracion del asistente. Todo valor ilegible es un error de
// despliegue; la ausencia de la clave no lo es.
func loadAssistantSettings() (assistantSettings, error) {
	var st assistantSettings
	var err error
	l := &st.config.Limits
	if l.MaxInputChars, err = config.EnvInt("WEBMAIL_ASSISTANT_MAX_INPUT_CHARS", defaultAssistantInputChars, 1000, maxAssistantInputChars); err != nil {
		return st, err
	}
	if l.MaxThreadMessages, err = config.EnvInt("WEBMAIL_ASSISTANT_MAX_THREAD_MESSAGES", defaultAssistantThread, 1, maxAssistantThread); err != nil {
		return st, err
	}
	if l.MaxInstructionChars, err = config.EnvInt("WEBMAIL_ASSISTANT_MAX_INSTRUCTION_CHARS", defaultAssistantInstruction, 1, maxAssistantInstruction); err != nil {
		return st, err
	}
	if l.MailboxDaily, err = config.EnvInt("WEBMAIL_ASSISTANT_MAILBOX_DAILY_LIMIT", defaultAssistantMailboxDay, 1, maxAssistantDaily); err != nil {
		return st, err
	}
	if l.TenantDaily, err = config.EnvInt("WEBMAIL_ASSISTANT_TENANT_DAILY_LIMIT", defaultAssistantTenantDay, 1, maxAssistantDaily); err != nil {
		return st, err
	}
	if err := l.Validate(); err != nil {
		return st, err
	}
	if st.config.SettingsTTL, err = config.EnvDuration("WEBMAIL_ASSISTANT_SETTINGS_TTL", defaultAssistantSettingsTTL, time.Second, 10*time.Minute); err != nil {
		return st, err
	}

	key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	if key == "" {
		return st, nil
	}
	p := &anthropic.Config{APIKey: key}
	p.BaseURL = envString("WEBMAIL_ASSISTANT_API_URL", defaultAssistantAPIURL)
	if u, err := url.Parse(p.BaseURL); err != nil || (u.Scheme != "https" && !config.DeclaredDevelopmentOrTest()) {
		return st, errors.New("WEBMAIL_ASSISTANT_API_URL debe ser https: la clave y el texto del correo viajan en ella (http solo con ENVIRONMENT development o test)")
	}
	p.Model = envString("WEBMAIL_ASSISTANT_MODEL", defaultAssistantModel)
	p.DraftModel = envString("WEBMAIL_ASSISTANT_DRAFT_MODEL", p.Model)
	if p.MaxOutputTokens, err = config.EnvInt("WEBMAIL_ASSISTANT_MAX_OUTPUT_TOKENS", defaultAssistantMaxTokens, 64, maxAssistantMaxTokens); err != nil {
		return st, err
	}
	if p.Timeout, err = config.EnvDuration("WEBMAIL_ASSISTANT_TIMEOUT", defaultAssistantTimeout, time.Second, 2*time.Minute); err != nil {
		return st, err
	}
	if p.MaxAttempts, err = config.EnvInt("WEBMAIL_ASSISTANT_MAX_ATTEMPTS", defaultAssistantAttempts, 1, maxAssistantAttempts); err != nil {
		return st, err
	}
	st.provider = p
	return st, nil
}

// newAssistant cablea el caso de uso del asistente. Sin clave, Provider queda nil.
func newAssistant(st assistantSettings, d app.AssistantDeps, logger *zap.Logger) (*app.AssistantService, error) {
	if st.provider != nil {
		client, err := anthropic.New(*st.provider, logger)
		if err != nil {
			return nil, fmt.Errorf("asistente: %w", err)
		}
		d.Provider = client
		logger.Info("webmail: asistente disponible para las empresas que lo activen",
			zap.String("model", st.provider.Model), zap.String("draft_model", st.provider.DraftModel))
	} else {
		logger.Info("webmail: asistente no disponible (sin ANTHROPIC_API_KEY en el almacen de secretos)")
	}
	d.Config = st.config
	d.Logger = logger
	return app.NewAssistantService(d)
}

var (
	_ ports.AssistantProvider = (*anthropic.Client)(nil)
	_ ports.AssistantSource   = (*app.Service)(nil)
)
