import { create } from 'zustand';
import {
  dropSession,
  onUnauthorized,
  restoreSession,
  setAccessToken,
  startSessionKeepAlive,
  stopSessionKeepAlive,
  clearStepUpToken,
} from '@/api/client';
import { identityApi, type LoginRequest, type SessionTokens, type User } from '@/api/identity';
import { useAccessStore } from '@/access/store';
import { decodeAccessClaims } from '@/lib/jwt';

export type AuthStatus = 'hydrating' | 'anonymous' | 'authenticated';

export interface AuthState {
  status: AuthStatus;
  userId: string | null;
  tenantId: string | null;
  roles: string[];
  user: User | null;
  /** Token del reto MFA: el login devolvio mfa_required y falta el codigo. */
  mfaToken: string | null;
  /** La sesion se cerro sin que el usuario lo pidiera (renovacion imposible). */
  sessionExpired: boolean;
  hydrate: () => Promise<void>;
  login: (input: LoginRequest) => Promise<void>;
  completeMfa: (code: string) => Promise<void>;
  cancelMfa: () => void;
  logout: () => Promise<void>;
  /**
   * Cierra la sesion solo en el cliente. Para cuando el servidor ya revoco todas las
   * sesiones del usuario (cambio de contrasena, MFA desactivada) y llamar a logout
   * solo produciria un 401.
   */
  endSession: () => void;
  acknowledgeExpired: () => void;
  setUser: (user: User) => void;
  reloadUser: () => Promise<void>;
}

const anonymous = {
  status: 'anonymous' as AuthStatus,
  userId: null,
  tenantId: null,
  roles: [] as string[],
  user: null,
  mfaToken: null,
};

export const useAuthStore = create<AuthState>((set, get) => {
  // Establece la sesion a partir de un access token valido: claims primero (la interfaz
  // ya puede decidir), y despues, en paralelo, la ficha del usuario y su acceso.
  const establish = async (token: string): Promise<void> => {
    const claims = decodeAccessClaims(token);
    if (!claims?.uid) {
      setAccessToken(null);
      set({ ...anonymous });
      return;
    }
    setAccessToken(token);
    set({
      status: 'authenticated',
      userId: claims.uid,
      tenantId: claims.tid ?? null,
      roles: claims.roles ?? [],
      mfaToken: null,
      sessionExpired: false,
    });
    startSessionKeepAlive();
    const [userRes] = await Promise.all([
      identityApi.getUser(claims.uid).catch(() => null),
      useAccessStore.getState().load(claims.uid),
    ]);
    if (userRes) set({ user: userRes.data });
  };

  const finishLogin = async (tokens: SessionTokens): Promise<void> => {
    if (tokens.mfa_required && tokens.mfa_token) {
      set({ mfaToken: tokens.mfa_token });
      return;
    }
    if (!tokens.access_token) {
      set({ ...anonymous });
      return;
    }
    await establish(tokens.access_token);
  };

  const clearLocal = (expired: boolean): void => {
    stopSessionKeepAlive();
    clearStepUpToken();
    useAccessStore.getState().reset();
    set({ ...anonymous, sessionExpired: expired });
  };

  onUnauthorized(() => {
    if (get().status === 'authenticated') clearLocal(true);
  });

  return {
    ...anonymous,
    status: 'hydrating',
    sessionExpired: false,

    hydrate: async () => {
      try {
        const token = await restoreSession();
        if (token) {
          await establish(token);
          return;
        }
        set({ ...anonymous });
      } catch {
        set({ ...anonymous });
      }
    },

    login: async (input) => {
      const { data } = await identityApi.login(input);
      await finishLogin(data);
    },

    completeMfa: async (code) => {
      const mfaToken = get().mfaToken;
      if (!mfaToken) return;
      const { data } = await identityApi.mfaChallenge(mfaToken, code);
      await finishLogin(data);
    },

    cancelMfa: () => set({ mfaToken: null }),

    logout: async () => {
      try {
        await identityApi.logout();
      } catch {
        // La sesion local se cierra igual: el access token muere en minutos y la cookie
        // la borra el servidor cuando la peticion llega.
      }
      dropSession();
      clearLocal(false);
    },

    endSession: () => {
      dropSession();
      clearLocal(false);
    },

    acknowledgeExpired: () => set({ sessionExpired: false }),

    setUser: (user) => set({ user }),

    reloadUser: async () => {
      const userId = get().userId;
      if (!userId) return;
      const { data } = await identityApi.getUser(userId);
      set({ user: data });
    },
  };
});
