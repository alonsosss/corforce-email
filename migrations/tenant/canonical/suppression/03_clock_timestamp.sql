-- Schema: suppression | Service: suppression
--
-- created_at es la hora de alta de cada causa y contacts la compara con el occurred_at de
-- contacts.consents, que ya es clock_timestamp(): una baja anterior a un reconsentimiento
-- no lo revoca y la resuscripcion solo retira la baja anterior a ella. Con DEFAULT now()
-- la fila llevaba la hora del INICIO de su transaccion: un alta que empezo, espero el
-- bloqueo de la direccion mientras la persona confirmaba un doble opt-in y se escribio
-- despues quedaba fechada antes que ese consentimiento. clock_timestamp() es la hora de la
-- sentencia que escribe la fila. updated_at pasa al mismo reloj para que una fila recien
-- creada no quede con updated_at anterior a created_at.
--
-- Solo cambia el valor por defecto de las filas nuevas; las existentes no se tocan.
-- Idempotente: fijar el mismo DEFAULT otra vez no cambia nada.

ALTER TABLE suppression.entries ALTER COLUMN created_at SET DEFAULT clock_timestamp();
ALTER TABLE suppression.entries ALTER COLUMN updated_at SET DEFAULT clock_timestamp();
