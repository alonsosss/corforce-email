-- Schema: mail_files | Service: mail-files
--
-- Ficheros grandes que un buzon comparte por enlace (docs/adr/0014). El contenido vive en el almacen
-- de objetos (object_key, espacio private/ que el gateway nunca sirve) y aqui solo sus metadatos:
-- dueno, nombre saneado, tamano, huella, caducidad, tope y cuenta de descargas y estado. El buzon dueno
-- vive en la celda (mail-directory): solo se guarda su id, sin clave foranea entre esquemas.
--
-- Estados guardados: pending (analizado por ClamAV y reservado en la cuota, subiendo al almacen),
-- ready (descargable mientras no caduque ni agote sus descargas), expired, revoked y failed (subida
-- que no termino). object_deleted_at marca que el barrido ya borro el objeto; la fila se conserva un
-- tiempo como historial del remitente y despues se borra.
--
-- Idempotente y aditiva.

CREATE SCHEMA IF NOT EXISTS mail_files;

CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE IF NOT EXISTS mail_files.shared_files (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    mailbox_id uuid NOT NULL,
    file_name varchar(255) NOT NULL,
    size_bytes bigint NOT NULL,
    sha256 char(64) NOT NULL,
    object_key varchar(300) NOT NULL,
    status varchar(16) NOT NULL DEFAULT 'pending',
    expires_at timestamptz NOT NULL,
    max_downloads integer NOT NULL,
    downloads integer NOT NULL DEFAULT 0,
    last_download_at timestamptz,
    revoked_at timestamptz,
    object_deleted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mail_files_shared_files_status_check CHECK (status IN ('pending', 'ready', 'expired', 'revoked', 'failed')),
    CONSTRAINT mail_files_shared_files_size_check CHECK (size_bytes > 0),
    CONSTRAINT mail_files_shared_files_sha256_check CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT mail_files_shared_files_name_check CHECK (length(file_name) > 0),
    CONSTRAINT mail_files_shared_files_downloads_check CHECK (max_downloads >= 1 AND downloads >= 0 AND downloads <= max_downloads),
    CONSTRAINT mail_files_shared_files_key_unique UNIQUE (object_key)
);

DO $$
BEGIN
    CREATE TRIGGER trg_mail_files_shared_files_updated
        BEFORE UPDATE ON mail_files.shared_files
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Listado del remitente, del mas reciente al mas antiguo.
CREATE INDEX IF NOT EXISTS idx_mail_files_shared_files_owner
    ON mail_files.shared_files (tenant_id, mailbox_id, created_at DESC);

-- Cuota, caducidad y borrado de objetos: solo las filas que aun ocupan el almacen.
CREATE INDEX IF NOT EXISTS idx_mail_files_shared_files_live
    ON mail_files.shared_files (tenant_id, expires_at)
    WHERE object_deleted_at IS NULL;

-- Historial que el barrido poda.
CREATE INDEX IF NOT EXISTS idx_mail_files_shared_files_history
    ON mail_files.shared_files (tenant_id, object_deleted_at)
    WHERE object_deleted_at IS NOT NULL;
