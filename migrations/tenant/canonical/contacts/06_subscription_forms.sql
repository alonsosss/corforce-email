-- Schema: contacts | Service: contacts
--
-- Formularios de suscripcion de la empresa (docs/Plan_Marketing_Avanzado.md, 2-F): se incrustan
-- por script o iframe en los sitios de la empresa y en sus paginas de aterrizaje, y cada envio
-- valido pide el doble opt-in (obligatorio: no hay columna para desactivarlo).
--
-- * subscription_forms: la definicion. fields es la lista ordenada [{key, label, required,
--   placeholder}] con key email, first_name, last_name o la clave de un atributo declarado; la
--   valida el servicio. allowed_origins son los origenes https que pueden incrustarlo (CSP
--   frame-ancestors) y llamar a su API publica (CORS). list_id es la lista a la que entra quien
--   confirma: una lista con formularios no se borra (RESTRICT, ademas de la comprobacion del
--   servicio).
-- * form_submissions: un envio valido por fila, para las estadisticas y para anadir a la lista a
--   quien confirma. Sin la direccion ni los campos enviados: el contacto (si se borra, la fila
--   queda sin vinculo) y el token del doble opt-in que se pidio (nulo si no se pidio). La
--   evidencia del consentimiento (texto aceptado, ip truncada) vive en contacts.consents.
--
-- Idempotente y aditiva: tablas nuevas cubiertas por el ALTER DEFAULT PRIVILEGES de
-- 04_service_role.sql.

CREATE TABLE IF NOT EXISTS contacts.subscription_forms (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    name varchar(200) NOT NULL,
    status varchar(10) NOT NULL DEFAULT 'active',
    list_id uuid NOT NULL REFERENCES contacts.lists (id) ON DELETE RESTRICT,
    fields jsonb NOT NULL,
    title varchar(200) NOT NULL DEFAULT '',
    description varchar(2000) NOT NULL DEFAULT '',
    submit_label varchar(60) NOT NULL DEFAULT '',
    consent_text varchar(2000) NOT NULL,
    success_message varchar(1000) NOT NULL,
    redirect_url varchar(2048),
    allowed_origins text[] NOT NULL DEFAULT '{}',
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT contacts_subscription_forms_tenant_name_key UNIQUE (tenant_id, name),
    CONSTRAINT contacts_subscription_forms_status_check CHECK (status IN ('active', 'disabled')),
    CONSTRAINT contacts_subscription_forms_fields_array CHECK (jsonb_typeof(fields) = 'array'),
    CONSTRAINT contacts_subscription_forms_consent_check CHECK (length(btrim(consent_text)) > 0),
    CONSTRAINT contacts_subscription_forms_redirect_check CHECK (redirect_url IS NULL OR redirect_url LIKE 'https://%'),
    CONSTRAINT contacts_subscription_forms_origins_check CHECK (cardinality(allowed_origins) <= 20)
);

CREATE INDEX IF NOT EXISTS idx_contacts_subscription_forms_list
    ON contacts.subscription_forms (tenant_id, list_id);

DO $$
BEGIN
    CREATE TRIGGER trg_contacts_subscription_forms_updated
        BEFORE UPDATE ON contacts.subscription_forms
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- outcome: confirmation_sent (se pidio el doble opt-in), already_subscribed (ya consentia; no se
-- envia nada) o not_reachable (la direccion esta excluida de todo envio: no recibe la
-- confirmacion). La respuesta publica es la misma en los tres casos.
CREATE TABLE IF NOT EXISTS contacts.form_submissions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    form_id uuid NOT NULL REFERENCES contacts.subscription_forms (id) ON DELETE CASCADE,
    contact_id uuid REFERENCES contacts.contacts (id) ON DELETE SET NULL,
    token_id uuid REFERENCES contacts.confirmation_tokens (id) ON DELETE SET NULL,
    list_id uuid REFERENCES contacts.lists (id) ON DELETE SET NULL,
    outcome varchar(20) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    confirmed_at timestamptz,
    CONSTRAINT contacts_form_submissions_outcome_check CHECK (
        outcome IN ('confirmation_sent', 'already_subscribed', 'not_reachable')
    ),
    CONSTRAINT contacts_form_submissions_confirmed_check CHECK (
        confirmed_at IS NULL OR outcome = 'confirmation_sent'
    )
);

CREATE INDEX IF NOT EXISTS idx_contacts_form_submissions_form
    ON contacts.form_submissions (tenant_id, form_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_contacts_form_submissions_token
    ON contacts.form_submissions (token_id) WHERE token_id IS NOT NULL;
