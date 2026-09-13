-- Schema: transactional | Service: transactional
--
-- Carril de marketing. Los mensajes de campana (POST /internal/transactional/batch, lo usa
-- campaigns) viven en la misma tabla que los transaccionales pero con su clase: salen por
-- su propia cola (transactional.marketing.queued), su configuration set y su tasa, y
-- reputation los juzga aparte. Nunca comparten reputacion ni carril.
--
-- Idempotente y aditiva: se puede reejecutar sobre cualquier base de empresa. Las columnas
-- nuevas con DEFAULT constante no reescriben la tabla.

ALTER TABLE transactional.messages ADD COLUMN IF NOT EXISTS class varchar(20) NOT NULL DEFAULT 'transactional';
ALTER TABLE transactional.messages ADD COLUMN IF NOT EXISTS campaign_id uuid;
ALTER TABLE transactional.messages ADD COLUMN IF NOT EXISTS contact_id uuid;

-- Las restricciones se crean una sola vez: volver a validarlas en cada reejecucion
-- recorreria la tabla entera con bloqueo exclusivo.
DO $$
BEGIN
    ALTER TABLE transactional.messages
        ADD CONSTRAINT messages_class_check CHECK (class IN ('transactional', 'marketing'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Un mensaje de marketing es siempre de una campana y de un contacto, lleva enlace de baja
-- y va a una sola persona (la baja y el seguimiento son por destinatario).
DO $$
BEGIN
    ALTER TABLE transactional.messages
        ADD CONSTRAINT messages_marketing_check CHECK (
            class <> 'marketing' OR (
                campaign_id IS NOT NULL
                AND contact_id IS NOT NULL
                AND unsubscribable
                AND jsonb_array_length("to") = 1
                AND cc = '[]'::jsonb
                AND bcc = '[]'::jsonb
            )
        );
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- campaigns y analytics consultan los mensajes de una campana.
CREATE INDEX IF NOT EXISTS idx_transactional_messages_tenant_campaign
    ON transactional.messages (tenant_id, campaign_id) WHERE campaign_id IS NOT NULL;

-- La clave de idempotencia identifica una peticion de una clase: la misma clave no puede
-- devolver mensajes transaccionales a quien pidio un lote de campana, ni al reves. Los
-- suprimidos se guardan para que una repeticion devuelva exactamente la misma respuesta.
ALTER TABLE transactional.submissions ADD COLUMN IF NOT EXISTS class varchar(20) NOT NULL DEFAULT 'transactional';
ALTER TABLE transactional.submissions ADD COLUMN IF NOT EXISTS suppressed jsonb NOT NULL DEFAULT '[]';

DO $$
BEGIN
    ALTER TABLE transactional.submissions
        ADD CONSTRAINT submissions_class_check CHECK (class IN ('transactional', 'marketing'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
