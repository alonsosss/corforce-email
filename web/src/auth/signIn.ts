import { ERROR_CODES, errorCode } from '@/api/errors';
import type { LoginRequest } from '@/api/identity';

export type SignInOutcome = 'mailbox' | 'platform';

/**
 * Inicio de sesion unico: la misma direccion y contrasena abren el buzon (webmail) o la
 * plataforma, segun a cual pertenezcan. Son dos credenciales distintas (mail-auth e identity) y
 * cada una cuenta sus fallos, asi que el orden importa:
 *
 *   - Primero el buzon. Quien entra a diario es la gente que lee su correo; probar antes la
 *     plataforma le contaria a identity un fallo por cada entrada (y, si la direccion tambien es
 *     de una cuenta de la plataforma, la acabaria bloqueando). El coste inverso es un fallo en
 *     el freno de mail-auth por cada entrada de un administrador, muy por debajo de su umbral.
 *   - Con la empresa indicada se entra solo a la plataforma: es la salida de quien administra y
 *     tiene un buzon con la misma direccion y la misma contrasena.
 *
 * El webmail se carga bajo demanda para no mezclar su cliente en el chunk de la plataforma.
 */
export async function signIn(
  input: LoginRequest,
  platformLogin: (input: LoginRequest) => Promise<void>,
): Promise<SignInOutcome> {
  if (input.tenant_slug) {
    await platformLogin(input);
    return 'platform';
  }

  let mailboxError: unknown;
  try {
    const { useWebmailStore } = await import('@/webmail/store');
    await useWebmailStore.getState().login(input.email, input.password);
    return 'mailbox';
  } catch (err) {
    mailboxError = err;
  }

  try {
    await platformLogin(input);
    return 'platform';
  } catch (platformError) {
    // Si la plataforma no reconoce la credencial pero el buzon no llego a comprobarla (servicio
    // caido o freno de intentos), ese es el motivo util: decir "contrasena incorrecta" a quien
    // solo tiene buzon seria falso.
    const mailboxChecked = errorCode(mailboxError) === ERROR_CODES.INVALID_CREDENTIALS;
    if (errorCode(platformError) === ERROR_CODES.UNAUTHORIZED && !mailboxChecked) {
      throw mailboxError;
    }
    throw platformError;
  }
}
