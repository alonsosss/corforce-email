// Lectura del access token SIN validarlo: la firma la comprueba el gateway. Aqui solo se
// leen los claims para saber cuando renovar y quien es el usuario de la sesion.

export interface AccessClaims {
  uid?: string;
  tid?: string;
  roles?: string[];
  exp?: number;
  iat?: number;
}

function decodeBase64Url(segment: string): string {
  const normalized = segment.replace(/-/g, '+').replace(/_/g, '/');
  const padded = normalized + '='.repeat((4 - (normalized.length % 4)) % 4);
  return atob(padded);
}

export function decodeAccessClaims(token: string): AccessClaims | null {
  try {
    const payload = token.split('.')[1];
    if (!payload) return null;
    const parsed: unknown = JSON.parse(decodeBase64Url(payload));
    if (typeof parsed !== 'object' || parsed === null) return null;
    const claims = parsed as Record<string, unknown>;
    return {
      uid: typeof claims.uid === 'string' ? claims.uid : undefined,
      tid: typeof claims.tid === 'string' ? claims.tid : undefined,
      roles: Array.isArray(claims.roles)
        ? claims.roles.filter((r): r is string => typeof r === 'string')
        : undefined,
      exp: typeof claims.exp === 'number' ? claims.exp : undefined,
      iat: typeof claims.iat === 'number' ? claims.iat : undefined,
    };
  } catch {
    return null;
  }
}

/**
 * Cierto cuando el token vence dentro del margen dado. Un token ilegible o sin exp cuenta
 * como vencido: nunca se manda un token del que no se sabe la vida.
 */
export function isTokenExpiring(token: string, marginSeconds: number, now = Date.now()): boolean {
  const claims = decodeAccessClaims(token);
  if (!claims?.exp) return true;
  return now / 1000 >= claims.exp - marginSeconds;
}
