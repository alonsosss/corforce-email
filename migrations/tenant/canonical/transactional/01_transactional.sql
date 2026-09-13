-- Schema: transactional | Service: transactional
--
-- Correo transaccional de la empresa: mensajes aceptados por el API o por la propia
-- plataforma, su ciclo de vida frente a Amazon SES, los eventos que SES devuelve por SNS,
-- la proyeccion local de los dominios de envio (alimentada por los eventos de
-- domain-service, nunca por lectura de su esquema) y el rastro de bajas RFC 8058.
--
-- Idempotente y aditiva: se puede reejecutar sobre cualquier base de empresa.

CREATE SCHEMA IF NOT EXISTS transactional;

-- Trigger de updated_at. Misma definicion que en los demas esquemas de la base de
-- empresa; CREATE OR REPLACE permite que cualquiera la aplique en cualquier orden.
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Un mensaje por destinatario final cuando se renderiza una plantilla o cuando lleva
-- enlace de baja (el seguimiento y la baja son por persona); un mensaje con todos los
-- destinatarios cuando el cuerpo llega crudo y no es dable de baja.
--
-- status: accepted (programado, espera su hora), queued (en la cola durable), sent
-- (SES lo acepto), delivered / bounced / complained / rejected (eventos de SES), failed
-- (SES lo rechazo de forma definitiva o se agotaron los reintentos), suppressed (todos
-- los destinatarios estaban en la lista de supresion: nunca se encolo).
CREATE TABLE IF NOT EXISTS transactional.messages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    submission_id uuid,
    idempotency_key text,
    from_email varchar(320) NOT NULL,
    from_name text NOT NULL DEFAULT '',
    reply_to varchar(320),
    "to" jsonb NOT NULL DEFAULT '[]',
    cc jsonb NOT NULL DEFAULT '[]',
    bcc jsonb NOT NULL DEFAULT '[]',
    subject text NOT NULL DEFAULT '',
    template_id uuid,
    template_version integer,
    variables jsonb NOT NULL DEFAULT '{}',
    html text,
    text text,
    headers jsonb NOT NULL DEFAULT '{}',
    tags jsonb NOT NULL DEFAULT '{}',
    unsubscribable boolean NOT NULL DEFAULT false,
    status varchar(20) NOT NULL DEFAULT 'queued',
    ses_message_id text,
    error text,
    attempts integer NOT NULL DEFAULT 0,
    scheduled_at timestamptz,
    sent_at timestamptz,
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT messages_status_check CHECK (status IN (
        'accepted', 'queued', 'sent', 'delivered', 'bounced', 'complained',
        'rejected', 'failed', 'suppressed'
    )),
    CONSTRAINT messages_body_check CHECK (template_id IS NOT NULL OR html IS NOT NULL OR text IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS idx_transactional_messages_tenant_created
    ON transactional.messages (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_transactional_messages_tenant_status
    ON transactional.messages (tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_transactional_messages_ses_message_id
    ON transactional.messages (ses_message_id);
CREATE INDEX IF NOT EXISTS idx_transactional_messages_idempotency
    ON transactional.messages (tenant_id, idempotency_key);
-- El liberador de programados solo mira lo que espera su hora.
CREATE INDEX IF NOT EXISTS idx_transactional_messages_scheduled
    ON transactional.messages (tenant_id, scheduled_at) WHERE status = 'accepted';

DO $$
BEGIN
    CREATE TRIGGER trg_transactional_messages_updated
        BEFORE UPDATE ON transactional.messages
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Una peticion de envio con clave de idempotencia. Existe aparte de messages porque una
-- peticion con plantilla produce un mensaje por destinatario: la clave identifica a la
-- peticion, no a cada mensaje, y repetirla debe devolver el mismo conjunto.
CREATE TABLE IF NOT EXISTS transactional.submissions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    idempotency_key text NOT NULL,
    message_ids jsonb NOT NULL DEFAULT '[]',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT submissions_tenant_key UNIQUE (tenant_id, idempotency_key)
);

-- Eventos del ciclo de vida: el envio propio (send, al aceptar SES la peticion) y los que
-- SES devuelve por SNS. sns_message_id es la clave de idempotencia de la ingesta: SNS
-- reintenta y una misma notificacion no puede contarse dos veces.
CREATE TABLE IF NOT EXISTS transactional.events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    message_id uuid NOT NULL REFERENCES transactional.messages(id) ON DELETE CASCADE,
    type varchar(20) NOT NULL,
    recipient varchar(320) NOT NULL DEFAULT '',
    detail jsonb NOT NULL DEFAULT '{}',
    sns_message_id text,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT events_type_check CHECK (type IN (
        'send', 'delivery', 'bounce', 'complaint', 'reject', 'delivery_delay',
        'open', 'click', 'rendering_failure', 'subscription'
    )),
    CONSTRAINT events_sns_message_id_key UNIQUE (sns_message_id)
);

CREATE INDEX IF NOT EXISTS idx_transactional_events_message_occurred
    ON transactional.events (message_id, occurred_at);

-- Proyeccion de los dominios de envio de la empresa, alimentada por los eventos
-- domains.domain.verified|failed|deleted. Solo se envia desde un remitente cuyo dominio
-- este verified con purpose sending o both.
CREATE TABLE IF NOT EXISTS transactional.sending_domains (
    tenant_id uuid NOT NULL,
    domain varchar(253) NOT NULL,
    status varchar(20) NOT NULL,
    purpose varchar(20) NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT sending_domains_pkey PRIMARY KEY (tenant_id, domain),
    CONSTRAINT sending_domains_domain_lower CHECK (domain = lower(domain))
);

-- Rastro de bajas por enlace RFC 8058. La supresion efectiva vive en suppression; aqui
-- queda constancia de que mensaje origino la baja.
CREATE TABLE IF NOT EXISTS transactional.unsubscribes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    email varchar(320) NOT NULL,
    message_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT unsubscribes_tenant_message_email_key UNIQUE (tenant_id, message_id, email)
);

CREATE INDEX IF NOT EXISTS idx_transactional_unsubscribes_tenant_email
    ON transactional.unsubscribes (tenant_id, email);
