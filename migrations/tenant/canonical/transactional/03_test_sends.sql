-- Schema: transactional | Service: transactional
--
-- Marca de envio de prueba. campaigns manda sus pruebas por el lote interno
-- (POST /internal/transactional/batch) con la etiqueta test=true; el servicio la guarda en
-- is_test al crear el mensaje y la publica como "test" en todos los eventos
-- transactional.email.*, para que la analitica no cuente esos envios. Solo el carril de
-- marketing la fija: la etiqueta test del API publico no marca nada, y la restriccion lo
-- garantiza tambien en la base.
--
-- Idempotente: la columna y el relleno de los mensajes de prueba ya existentes (lotes de
-- marketing con la etiqueta, los unicos que la marcan) ocurren una sola vez, al crear la
-- columna; reejecutar no vuelve a recorrer la tabla. DEFAULT constante: no la reescribe.

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
         WHERE table_schema = 'transactional' AND table_name = 'messages' AND column_name = 'is_test'
    ) THEN
        ALTER TABLE transactional.messages ADD COLUMN is_test boolean NOT NULL DEFAULT false;
        UPDATE transactional.messages SET is_test = true
         WHERE class = 'marketing' AND tags->>'test' = 'true';
    END IF;
END $$;

DO $$
BEGIN
    ALTER TABLE transactional.messages
        ADD CONSTRAINT messages_test_marketing_check CHECK (NOT is_test OR class = 'marketing');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
