-- Schema: transactional | Service: transactional
--
-- Envio de prueba de una version de plantilla (POST /internal/transactional/test-send, lo
-- usa templates desde el editor y el detalle de la plantilla). Hasta aqui solo las pruebas
-- de campana, por la via de marketing, podian marcarse como prueba. Una prueba de plantilla
-- sale por el carril de su tipo (una plantilla transaccional por el transaccional, una de
-- marketing por el de marketing, con su configuration set y su List-Unsubscribe) y no tiene
-- campana ni contacto.
--
-- messages_marketing_check admite, solo para una prueba, un mensaje de marketing sin
-- campana ni contacto; lo demas (baja, un destinatario, sin copias) sigue exigido.
--
-- messages_test_marketing_check pasa a messages_test_check: una prueba es siempre un mensaje
-- renderizado de una plantilla (template_version) para una sola persona, en cualquier clase.
-- La marca la siguen fijando solo las dos rutas internas; el API publico no la toca.
--
-- Idempotente sin volver a recorrer la tabla: cada restriccion se cambia una sola vez, cuando
-- todavia no tiene su forma nueva. Todas las filas existentes cumplen las dos formas nuevas
-- (son mas amplias que las anteriores), asi que el cambio nunca falla sobre datos reales.

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conrelid = 'transactional.messages'::regclass AND conname = 'messages_marketing_check'
           AND pg_get_constraintdef(oid) LIKE '%is_test%'
    ) THEN
        ALTER TABLE transactional.messages DROP CONSTRAINT IF EXISTS messages_marketing_check;
        ALTER TABLE transactional.messages
            ADD CONSTRAINT messages_marketing_check CHECK (
                class <> 'marketing' OR (
                    unsubscribable
                    AND jsonb_array_length("to") = 1
                    AND cc = '[]'::jsonb
                    AND bcc = '[]'::jsonb
                    AND (is_test OR (campaign_id IS NOT NULL AND contact_id IS NOT NULL))
                )
            );
    END IF;
END $$;

-- La restriccion anterior se retira siempre (retirar no recorre la tabla): reejecutar la
-- secuencia entera vuelve a crearla en 03_test_sends.sql y aqui se vuelve a quitar.
ALTER TABLE transactional.messages DROP CONSTRAINT IF EXISTS messages_test_marketing_check;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conrelid = 'transactional.messages'::regclass AND conname = 'messages_test_check'
    ) THEN
        ALTER TABLE transactional.messages
            ADD CONSTRAINT messages_test_check CHECK (
                NOT is_test OR (
                    template_version IS NOT NULL
                    AND jsonb_array_length("to") = 1
                    AND cc = '[]'::jsonb
                    AND bcc = '[]'::jsonb
                )
            );
    END IF;
END $$;

-- El tope de pruebas por hora cuenta las de la empresa en la ultima hora; sin este indice
-- recorreria todos sus envios recientes.
CREATE INDEX IF NOT EXISTS idx_transactional_messages_tenant_test_created
    ON transactional.messages (tenant_id, created_at) WHERE is_test;
