-- Schema: mail_security | Service: mail-security
--
-- Marca de modificacion de los documentos que los motores sondean con If-Modified-Since
-- (hoy /settings de Rspamd). Vivia en la memoria de cada proceso: con dos replicas, una
-- recien arrancada podia fechar el documento antes que la otra y responder 304 a un Rspamd
-- con reglas viejas, y por eso el servicio corria con UNA sola replica. Aqui la marca es
-- una por celda: la replica que ve un contenido distinto (por su hash) la adelanta bajo
-- FOR UPDATE y todas responden con la misma.
--
-- Sin tenant_id: el documento es uno para toda la celda. Solo lo toca el camino de los
-- motores, que corre como dueno; mail_app no tiene permisos ni politica sobre la tabla.
-- Idempotente y aditiva.

CREATE TABLE IF NOT EXISTS mail_security.engine_documents (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    document      text NOT NULL UNIQUE,
    content_hash  text NOT NULL,
    last_modified timestamptz NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger
         WHERE tgname = 'trg_mail_security_engine_documents_updated_at'
           AND tgrelid = 'mail_security.engine_documents'::regclass
    ) THEN
        CREATE TRIGGER trg_mail_security_engine_documents_updated_at BEFORE UPDATE ON mail_security.engine_documents
            FOR EACH ROW EXECUTE FUNCTION update_updated_at();
    END IF;
END $$;

-- La 01 concede por defecto CRUD sobre las tablas nuevas del esquema a mail_app: aqui se
-- retira, y RLS sin politica para mail_app lo deja fuera aunque alguien lo vuelva a dar.
REVOKE ALL ON mail_security.engine_documents FROM mail_app;
ALTER TABLE mail_security.engine_documents ENABLE ROW LEVEL SECURITY;
