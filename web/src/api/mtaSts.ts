/*
 * Politica MTA-STS (RFC 8461) de un dominio de la empresa: GET/PUT /mail-domains/mta-sts/{dominio} de
 * mail-directory (domain.MTASTSState). El modo sale del servicio y los pasos que ofrece tambien
 * (allowed_modes): la interfaz no copia las reglas de transicion.
 */

/** none no publica politica; testing la publica sin bloquear entregas; enforce las bloquea si el TLS falla. */
export type MtaStsMode = 'none' | 'testing' | 'enforce';

export interface MtaStsState {
  domain: string;
  /** El dominio esta verificado y activo en el directorio: solo asi se publica y admite enforce. */
  domain_active: boolean;
  mode: MtaStsMode;
  /** Segundos que un remitente conserva la politica. 0 mientras no haya politica. */
  max_age: number;
  /** Version de la politica: el id del TXT _mta-sts. Vacia mientras no haya politica. */
  policy_id: string;
  updated_at: string | null;
  /** Modos a los que puede pasar ahora. */
  allowed_modes: MtaStsMode[];
}
