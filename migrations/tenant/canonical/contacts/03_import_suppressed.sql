-- Schema: contacts | Service: contacts
--
-- Contactos que una importacion creo ya excluidos. La importacion consulta suppression por
-- lotes y cada contacto nuevo entra con el estado que implican las causas vigentes de su
-- direccion; suppressed cuenta, por estado, los creados que no entraron active ({"unsubscribed":
-- 2, "bounced": 1}), para que el rastro diga por que no cuentan en la audiencia. Esos contactos
-- nunca reciben el consentimiento de la importacion.
--
-- Idempotente y aditiva: la columna, con su CHECK de objeto, se anade solo si falta (al
-- re-ejecutarse Postgres salta la clausula entera), con '{}' en las importaciones anteriores,
-- que no comprobaban nada.

ALTER TABLE contacts.imports ADD COLUMN IF NOT EXISTS suppressed jsonb NOT NULL DEFAULT '{}'::jsonb
    CONSTRAINT contacts_imports_suppressed_object CHECK (jsonb_typeof(suppressed) = 'object');
