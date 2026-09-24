import { api } from './client';
import { endpoints } from './endpoints';
import type { PermissionTriple } from './access';

// DTOs de services/access-control/internal/adapters/http/api_keys.go.

export const API_KEY_ERRORS = {
  SCOPE_NOT_GRANTABLE: 'SCOPE_NOT_GRANTABLE',
  API_KEY_LIMIT: 'API_KEY_LIMIT',
  API_KEY_REVOKED: 'API_KEY_REVOKED',
} as const;

export const API_KEY_STATUS = {
  active: 'active',
  revoked: 'revoked',
  expired: 'expired',
} as const;

export type ApiKeyStatus = (typeof API_KEY_STATUS)[keyof typeof API_KEY_STATUS];

export interface ApiKeyScope extends PermissionTriple {
  description?: string;
}

export interface ApiKey {
  id: string;
  name: string;
  prefix: string;
  scopes: ApiKeyScope[];
  status: ApiKeyStatus;
  created_by: string;
  created_at: string;
  expires_at: string | null;
  revoked_at: string | null;
  revoked_by: string | null;
  last_used_at: string | null;
  last_used_ip: string;
}

export interface SmtpSettings {
  host: string;
  starttls_port: number;
  tls_port: number;
}

export interface ApiKeySettings {
  /** null cuando la plataforma no publica el relevo SMTP. */
  smtp: SmtpSettings | null;
}

export interface ApiKeyInput {
  name: string;
  scopes: PermissionTriple[];
  expires_at: string | null;
}

/** El secreto solo existe en esta respuesta: el servidor guarda su resumen. */
export interface CreatedApiKey {
  key: ApiKey;
  secret: string;
}

export const apiKeysApi = {
  list: async (): Promise<ApiKey[]> =>
    (await api.get<ApiKey[] | null>(endpoints.apiKeys.collection)).data ?? [],
  settings: async (): Promise<ApiKeySettings> =>
    (await api.get<ApiKeySettings>(endpoints.apiKeys.settings)).data,
  scopes: async (): Promise<ApiKeyScope[]> =>
    (await api.get<ApiKeyScope[] | null>(endpoints.apiKeys.scopes)).data ?? [],
  create: async (input: ApiKeyInput): Promise<CreatedApiKey> =>
    (await api.post<CreatedApiKey>(endpoints.apiKeys.collection, { body: input })).data,
  revoke: async (id: string): Promise<ApiKey> =>
    (await api.post<ApiKey>(endpoints.apiKeys.revoke(id))).data,
};
