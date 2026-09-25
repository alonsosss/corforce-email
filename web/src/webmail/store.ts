import { create } from 'zustand';
import { ERROR_CODES, errorCode } from '@/api/errors';
import {
  isMfaChallenge,
  onWebmailSessionExpired,
  webmailApi,
  type WebmailSession,
} from '@/api/webmail';
import { resetWebmailCatalogs } from './catalogs';

/*
 * Sesion del buzon en el webmail. Independiente de la de la plataforma (auth/store.ts):
 * otra cookie, otro servicio y otro estado. Solo guarda en memoria lo que el servicio
 * devuelve de la sesion (buzon, nombre, caducidad y cuota); ningun token.
 */

/** mfa: la contrasena fue correcta y falta el codigo de la verificacion en dos pasos. */
export type WebmailStatus = 'checking' | 'unavailable' | 'anonymous' | 'mfa' | 'authenticated';

export interface WebmailState {
  status: WebmailStatus;
  session: WebmailSession | null;
  /** La sesion cayo sin que el usuario la cerrara (inactividad, vida maxima, revocacion). */
  expired: boolean;
  /** La sesion termino porque el usuario cambio su contrasena: todas quedan revocadas. */
  passwordChanged: boolean;
  /** El desafio del segundo paso caduco o se agoto: hay que volver a escribir la contrasena. */
  mfaExpired: boolean;
  /** Buzon que espera el segundo paso; solo para mostrarlo. */
  mfaUsername: string | null;
  /** Por que no se pudo comprobar la sesion (el servicio no respondio). */
  checkError: unknown;
  check: () => Promise<void>;
  login: (username: string, password: string) => Promise<void>;
  /** Segundo paso: codigo TOTP o de recuperacion. */
  verifyMfa: (code: string) => Promise<void>;
  cancelMfa: () => void;
  logout: () => Promise<void>;
  /** Relee la sesion: la cuota cambia al enviar, guardar o borrar. */
  refresh: () => Promise<void>;
  /** Tras cambiar la contrasena: el servicio ya revoco la cookie, se vuelve al acceso. */
  endAfterPasswordChange: () => void;
  acknowledgeExpired: () => void;
}

const signedOut = { session: null, checkError: null, mfaUsername: null };

export const useWebmailStore = create<WebmailState>((set, get) => {
  let checking: Promise<void> | null = null;

  onWebmailSessionExpired(() => {
    if (get().status === 'authenticated') {
      resetWebmailCatalogs();
      set({ ...signedOut, status: 'anonymous', expired: true });
    }
  });

  const openSession = (session: WebmailSession) => {
    resetWebmailCatalogs();
    set({
      status: 'authenticated',
      session,
      expired: false,
      passwordChanged: false,
      mfaExpired: false,
      mfaUsername: null,
      checkError: null,
    });
    // El inicio de sesion no trae la cuota; se pide aparte sin bloquear la entrada.
    void get().refresh();
  };

  const runCheck = async (): Promise<void> => {
    set({ status: 'checking', checkError: null });
    try {
      const session = await webmailApi.session();
      set({ status: 'authenticated', session, expired: false, checkError: null });
    } catch (err) {
      if (errorCode(err) === ERROR_CODES.SESSION_EXPIRED) {
        resetWebmailCatalogs();
        set({ ...signedOut, status: 'anonymous' });
      } else {
        set({ ...signedOut, status: 'unavailable', checkError: err });
      }
    }
  };

  return {
    status: 'checking',
    session: null,
    expired: false,
    passwordChanged: false,
    mfaExpired: false,
    mfaUsername: null,
    checkError: null,

    check: () => {
      checking ??= runCheck().finally(() => {
        checking = null;
      });
      return checking;
    },

    login: async (username, password) => {
      const result = await webmailApi.login(username, password);
      if (isMfaChallenge(result)) {
        set({
          ...signedOut,
          status: 'mfa',
          mfaUsername: username,
          expired: false,
          passwordChanged: false,
          mfaExpired: false,
        });
        return;
      }
      openSession(result);
    },

    verifyMfa: async (code) => {
      try {
        openSession(await webmailApi.loginMfa(code));
      } catch (err) {
        if (errorCode(err) === ERROR_CODES.MFA_CHALLENGE_EXPIRED) {
          set({ ...signedOut, status: 'anonymous', mfaExpired: true });
        }
        throw err;
      }
    },

    cancelMfa: () => set({ ...signedOut, status: 'anonymous', mfaExpired: false }),

    logout: async () => {
      try {
        await webmailApi.logout();
      } catch (err) {
        // Si el servicio no pudo borrar la sesion, la cookie sigue valida: no se finge un
        // cierre. Una sesion ya caducada si cuenta como cerrada.
        if (errorCode(err) !== ERROR_CODES.SESSION_EXPIRED) throw err;
      }
      resetWebmailCatalogs();
      set({ ...signedOut, status: 'anonymous', expired: false });
    },

    refresh: async () => {
      if (get().status !== 'authenticated') return;
      try {
        const session = await webmailApi.session();
        if (get().status === 'authenticated') set({ session });
      } catch {
        // SESSION_EXPIRED ya lo trata el aviso del cliente; otro fallo deja la cuota previa.
      }
    },

    endAfterPasswordChange: () => {
      resetWebmailCatalogs();
      set({ ...signedOut, status: 'anonymous', expired: false, passwordChanged: true });
    },

    acknowledgeExpired: () => set({ expired: false, passwordChanged: false, mfaExpired: false }),
  };
});
