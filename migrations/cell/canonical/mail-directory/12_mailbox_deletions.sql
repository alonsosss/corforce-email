-- Schema: mail | Service: mail-directory
--
-- Marcas de baja de buzon para el barrido de maildir de Dovecot (deploy/mail/README.md, "Maildir de
-- un buzon borrado"). Borrar un buzon quita su fila de mail.mailboxes, pero su maildir
-- (/var/vmail/<dominio>/<local>/) sigue en el volumen de Dovecot: quien reciba despues la misma
-- direccion heredaria el correo del titular anterior. Ningun servicio Go alcanza ese volumen, asi que
-- mail-directory deja aqui una marca en la MISMA transaccion del borrado y un guion dentro del
-- contenedor de Dovecot (maildir_reconcile.sh) la consume: mueve el maildir a _garbage y borra la
-- marca. Es la unica tabla del esquema que el rol de los motores borra ademas de leer: una marca es
-- una orden para ese guion, sin credenciales ni contenido.
--
-- La funcion mailbox_deletion_pending responde a mail-directory si una direccion tiene una marca
-- reciente que el guion aun no consumio, de cualquier empresa de la celda: hasta entonces no se
-- vuelve a crear un buzon con ese nombre (409). SECURITY DEFINER porque mail_app solo ve las marcas
-- de su empresa y el maildir no sabe de empresas; devuelve un booleano y nada mas. Idempotente y
-- aditiva.

CREATE TABLE IF NOT EXISTS mail.mailbox_deletions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL,
    mailbox_id uuid NOT NULL,
    username   text NOT NULL,
    local_part text NOT NULL,
    domain     text NOT NULL,
    deleted_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mailbox_deletions_username_lower CHECK (username = lower(username)),
    CONSTRAINT mailbox_deletions_username_parts CHECK (username = local_part || '@' || domain)
);
CREATE INDEX IF NOT EXISTS idx_mail_mailbox_deletions_username ON mail.mailbox_deletions (username, deleted_at DESC);
CREATE INDEX IF NOT EXISTS idx_mail_mailbox_deletions_tenant ON mail.mailbox_deletions (tenant_id);

ALTER TABLE mail.mailbox_deletions ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON mail.mailbox_deletions;
CREATE POLICY tenant_isolation ON mail.mailbox_deletions FOR ALL TO mail_app
    USING (tenant_id = mail.current_tenant()) WITH CHECK (tenant_id = mail.current_tenant());
DROP POLICY IF EXISTS service_all ON mail.mailbox_deletions;
CREATE POLICY service_all ON mail.mailbox_deletions FOR ALL TO mail_service USING (true) WITH CHECK (true);
DROP POLICY IF EXISTS engine_all ON mail.mailbox_deletions;
CREATE POLICY engine_all ON mail.mailbox_deletions FOR ALL TO mail_engine USING (true) WITH CHECK (true);

-- La aplicacion solo deja marcas y las lee; el motor las lee y las consume.
REVOKE ALL ON mail.mailbox_deletions FROM mail_app, mail_engine;
GRANT SELECT, INSERT ON mail.mailbox_deletions TO mail_app;
GRANT SELECT, DELETE ON mail.mailbox_deletions TO mail_engine;

CREATE OR REPLACE FUNCTION mail.mailbox_deletion_pending(p_username text, p_hold interval) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = mail, pg_temp AS $$
    SELECT EXISTS (
        SELECT 1 FROM mail.mailbox_deletions
         WHERE username = p_username AND deleted_at > now() - p_hold
    )
$$;
REVOKE ALL ON FUNCTION mail.mailbox_deletion_pending(text, interval) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION mail.mailbox_deletion_pending(text, interval) TO mail_app;
