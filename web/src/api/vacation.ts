/*
 * Respuesta automatica de un buzon (mail-directory, 09_vacation.sql). La misma forma la sirven el
 * API de administracion (GET/PUT /mailboxes/{id}/vacation) y el del webmail (GET/PUT
 * /webmail/vacation). Los topes llegan del servicio en cada respuesta: la interfaz no los copia.
 */

export interface VacationLimits {
  subject_max_length: number;
  message_max_length: number;
  interval_min_days: number;
  interval_max_days: number;
}

export interface Vacation {
  enabled: boolean;
  subject: string;
  message: string;
  /** Una respuesta por remitente cada tantos dias. */
  interval_days: number;
  /** AAAA-MM-DD, con la fecha del servidor (UTC). null: sin limite. */
  starts_on: string | null;
  ends_on: string | null;
  updated_at: string | null;
  limits: VacationLimits;
}

/** Lo que el usuario cambia: el PUT reemplaza la respuesta entera. */
export interface VacationInput {
  enabled: boolean;
  subject: string;
  message: string;
  interval_days: number;
  starts_on: string | null;
  ends_on: string | null;
}

export function toVacationInput(v: Vacation): VacationInput {
  return {
    enabled: v.enabled,
    subject: v.subject,
    message: v.message,
    interval_days: v.interval_days,
    starts_on: v.starts_on,
    ends_on: v.ends_on,
  };
}
