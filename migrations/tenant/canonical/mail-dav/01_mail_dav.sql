-- Schema: mail_dav | Service: mail-dav
--
-- Libretas de contactos personales de cada buzon (CardDAV, docs/adr/0004). Los contactos son datos de la
-- empresa y viven en su base; el buzon dueno vive en la celda (mail-directory), asi que aqui solo se
-- guarda su id, sin clave foranea entre esquemas.
--
-- Aislamiento: un buzon ve solo sus libretas. Lo da tenant_id y mailbox_id en cada consulta y, debajo,
-- la politica RLS de 02_service_role.sql, que compara las dos columnas con la empresa y el buzon de la
-- sesion (app.current_tenant_id y app.current_user_id, que fija pkg/db al abrir la transaccion).
--
-- Un vCard se guarda tal cual lo envio el cliente (vcard), con su etag (SHA-256 de esos bytes) y los
-- campos indexados que hacen falta para listarlo. collection_changes es el registro de cambios de cada
-- libreta para sync-collection (RFC 6578); se poda a los ultimos cambios y changes_floor marca el
-- limite: un token anterior ya no se resuelve y el cliente vuelve a listar.
--
-- Idempotente y aditiva: se puede reejecutar sobre cualquier base de empresa.

CREATE SCHEMA IF NOT EXISTS mail_dav;

CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE IF NOT EXISTS mail_dav.addressbooks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    mailbox_id uuid NOT NULL,
    slug varchar(63) NOT NULL,
    display_name varchar(200) NOT NULL,
    description varchar(1000) NOT NULL DEFAULT '',
    sync_seq bigint NOT NULL DEFAULT 0,
    changes_floor bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mail_dav_addressbooks_slug_check CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    CONSTRAINT mail_dav_addressbooks_seq_check CHECK (sync_seq >= 0 AND changes_floor >= 0 AND changes_floor <= sync_seq),
    CONSTRAINT mail_dav_addressbooks_slug_unique UNIQUE (tenant_id, mailbox_id, slug),
    CONSTRAINT mail_dav_addressbooks_owner_unique UNIQUE (id, tenant_id, mailbox_id)
);

DO $$
BEGIN
    CREATE TRIGGER trg_mail_dav_addressbooks_updated
        BEFORE UPDATE ON mail_dav.addressbooks
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- La clave foranea lleva empresa y buzon: un contacto no puede apuntar a la libreta de otro buzon.
CREATE TABLE IF NOT EXISTS mail_dav.contacts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    mailbox_id uuid NOT NULL,
    addressbook_id uuid NOT NULL,
    resource_name varchar(200) NOT NULL,
    uid varchar(255) NOT NULL,
    vcard text NOT NULL,
    etag char(64) NOT NULL,
    display_name varchar(300) NOT NULL DEFAULT '',
    emails text[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mail_dav_contacts_addressbook_fk FOREIGN KEY (addressbook_id, tenant_id, mailbox_id)
        REFERENCES mail_dav.addressbooks (id, tenant_id, mailbox_id) ON DELETE CASCADE,
    CONSTRAINT mail_dav_contacts_resource_check CHECK (resource_name ~ '^[A-Za-z0-9][A-Za-z0-9._@=+~-]{0,195}\.vcf$'),
    CONSTRAINT mail_dav_contacts_etag_check CHECK (etag ~ '^[0-9a-f]{64}$'),
    CONSTRAINT mail_dav_contacts_size_check CHECK (octet_length(vcard) <= 4194304),
    CONSTRAINT mail_dav_contacts_resource_unique UNIQUE (addressbook_id, resource_name),
    CONSTRAINT mail_dav_contacts_uid_unique UNIQUE (addressbook_id, uid)
);

DO $$
BEGIN
    CREATE TRIGGER trg_mail_dav_contacts_updated
        BEFORE UPDATE ON mail_dav.contacts
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- El limite de contactos por buzon cuenta por aqui.
CREATE INDEX IF NOT EXISTS idx_mail_dav_contacts_mailbox ON mail_dav.contacts (tenant_id, mailbox_id);

CREATE TABLE IF NOT EXISTS mail_dav.collection_changes (
    addressbook_id uuid NOT NULL,
    seq bigint NOT NULL,
    tenant_id uuid NOT NULL,
    mailbox_id uuid NOT NULL,
    resource_name varchar(200) NOT NULL,
    deleted boolean NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (addressbook_id, seq),
    CONSTRAINT mail_dav_changes_addressbook_fk FOREIGN KEY (addressbook_id, tenant_id, mailbox_id)
        REFERENCES mail_dav.addressbooks (id, tenant_id, mailbox_id) ON DELETE CASCADE,
    CONSTRAINT mail_dav_changes_seq_check CHECK (seq > 0)
);

CREATE INDEX IF NOT EXISTS idx_mail_dav_changes_resource ON mail_dav.collection_changes (addressbook_id, resource_name, seq DESC);
