-- Schema: scheduler | Service: scheduler
-- Version de la definicion de un trabajo, para la concurrencia optimista de la edicion: el
-- PUT lleva la version que leyo y solo se aplica si sigue siendo la guardada, que sube en
-- uno. Dos administradores que editan a la vez ya no se pisan: el segundo recibe 409 y
-- relee.
--
-- Es un entero y no updated_at: el trigger update_updated_at lo pisa con NOW(), que es la
-- hora de inicio de la transaccion (dos escrituras pueden compartirla y un reloj que
-- retrocede la repite), y el navegador lee las fechas con milisegundos, no microsegundos.
-- Solo la sube la edicion de la definicion: activar, desactivar y el calendario cambian
-- is_active, que el PUT no escribe, asi que no invalidan una edicion en curso.
--
-- Las filas existentes empiezan en 1, como un alta. Idempotente y solo aditiva.

ALTER TABLE scheduler.job_definitions ADD COLUMN IF NOT EXISTS version bigint NOT NULL DEFAULT 1;

DO $$
BEGIN
    ALTER TABLE scheduler.job_definitions ADD CONSTRAINT job_definitions_version_check
        CHECK (version >= 1);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
