import { api } from './client';
import { endpoints } from './endpoints';
import { toPage, type Page, type PageQuery } from './types';

// DTOs de services/identity/internal/adapters/http/handler.go.

export type UserStatus = 'active' | 'inactive' | 'locked';

export interface User {
  id: string;
  email: string;
  first_name: string;
  last_name: string;
  avatar_url: string | null;
  status: UserStatus;
  mfa_enabled: boolean;
  last_login_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface LoginRequest {
  email: string;
  password: string;
  tenant_slug?: string;
}

export interface SessionTokens {
  access_token?: string;
  expires_in?: number;
  token_type?: string;
  user_id?: string;
  tenant_id?: string;
  roles?: string[];
  mfa_required?: boolean;
  mfa_token?: string;
}

export interface PasswordRules {
  min_length: number;
  require_uppercase: boolean;
  require_lowercase: boolean;
  require_digit: boolean;
  require_special: boolean;
  breach_check: boolean;
}

export interface CreateUserRequest {
  email: string;
  password: string;
  first_name: string;
  last_name: string;
}

export interface UpdateUserRequest {
  first_name?: string;
  last_name?: string;
  status?: Extract<UserStatus, 'active' | 'inactive'>;
  avatar_url?: string;
}

export interface UserListQuery extends PageQuery {
  search?: string;
}

export interface SessionInfo {
  id: string;
  user_id: string;
  user_name: string;
  user_email: string;
  tenant_id: string;
  tenant_name?: string;
  ip_address: string;
  user_agent: string;
  login_at: string;
  last_seen_at: string;
  expires_at: string;
  revoked: boolean;
  revoked_at?: string;
}

export interface SessionListQuery extends PageQuery {
  user_id?: string;
  active?: boolean;
  tenant_id?: string;
}

export interface SessionPolicy {
  tenant_id: string;
  refresh_ttl_hours: number;
  max_concurrent_sessions: number;
  idle_timeout_minutes: number;
  updated_at: string;
  updated_by?: string;
}

export interface SessionPolicyInput {
  refresh_ttl_hours: number;
  max_concurrent_sessions: number;
  idle_timeout_minutes: number;
}

export interface MfaSetupResponse {
  secret: string;
  provisioning_uri: string;
}

export interface StepUpResponse {
  step_up_token: string;
  expires_in: number;
}

interface StatusResponse {
  status: string;
}

interface MessageResponse {
  message: string;
}

const DEFAULT_PAGE = { page: 1, per_page: 20 } as const;

export const identityApi = {
  login: (input: LoginRequest) =>
    api.post<SessionTokens>(endpoints.auth.login, { body: { ...input, cookie_auth: true } }),

  mfaChallenge: (mfaToken: string, code: string) =>
    api.post<SessionTokens>(endpoints.auth.mfaChallenge, {
      body: { mfa_token: mfaToken, code, cookie_auth: true },
    }),

  forgotPassword: (email: string) =>
    api.post<MessageResponse>(endpoints.auth.forgotPassword, { body: { email } }),

  resetPasswordPolicy: (token: string) =>
    api.get<PasswordRules>(endpoints.auth.resetPasswordPolicy, { params: { token } }),

  resetPassword: (token: string, newPassword: string) =>
    api.post<MessageResponse>(endpoints.auth.resetPassword, {
      body: { token, new_password: newPassword },
    }),

  stepUp: (currentPassword: string, code: string) =>
    api.post<StepUpResponse>(endpoints.auth.stepUp, {
      body: { current_password: currentPassword, code },
    }),

  mfaSetup: () => api.post<MfaSetupResponse>(endpoints.auth.mfaSetup),

  mfaActivate: (secret: string, code: string) =>
    api.post<StatusResponse>(endpoints.auth.mfaActivate, { body: { secret, code } }),

  mfaDisable: (currentPassword: string, code: string) =>
    api.delete<StatusResponse>(endpoints.auth.mfaDisable, {
      body: { current_password: currentPassword, code },
    }),

  logout: () => api.post<StatusResponse>(endpoints.sessions.logout),

  logoutAll: () => api.post<StatusResponse>(endpoints.sessions.logoutAll),

  getUser: (id: string) => api.get<User>(endpoints.users.byId(id)),

  listUsers: async (query: UserListQuery): Promise<Page<User>> =>
    toPage(await api.get<User[]>(endpoints.users.collection, { params: { ...query } }), {
      page: query.page ?? DEFAULT_PAGE.page,
      per_page: query.per_page ?? DEFAULT_PAGE.per_page,
    }),

  createUser: (input: CreateUserRequest) =>
    api.post<User>(endpoints.users.collection, { body: input }),

  updateUser: (id: string, input: UpdateUserRequest) =>
    api.patch<User>(endpoints.users.byId(id), { body: input }),

  deactivateUser: (id: string) => api.post<StatusResponse>(endpoints.users.deactivate(id)),

  deleteUser: (id: string) => api.delete<null>(endpoints.users.byId(id)),

  adminResetPassword: (id: string, newPassword: string) =>
    api.post<StatusResponse>(endpoints.users.resetPassword(id), {
      body: { new_password: newPassword },
    }),

  changePassword: (currentPassword: string, newPassword: string) =>
    api.post<StatusResponse>(endpoints.users.changePassword, {
      body: { current_password: currentPassword, new_password: newPassword },
    }),

  passwordPolicy: () => api.get<PasswordRules>(endpoints.users.passwordPolicy),

  mySessions: async (query: SessionListQuery): Promise<Page<SessionInfo>> =>
    toPage(await api.get<SessionInfo[]>(endpoints.sessions.mine, { params: { ...query } }), {
      page: query.page ?? DEFAULT_PAGE.page,
      per_page: query.per_page ?? DEFAULT_PAGE.per_page,
    }),

  listSessions: async (query: SessionListQuery): Promise<Page<SessionInfo>> =>
    toPage(await api.get<SessionInfo[]>(endpoints.sessions.collection, { params: { ...query } }), {
      page: query.page ?? DEFAULT_PAGE.page,
      per_page: query.per_page ?? DEFAULT_PAGE.per_page,
    }),

  listPlatformSessions: async (query: SessionListQuery): Promise<Page<SessionInfo>> =>
    toPage(await api.get<SessionInfo[]>(endpoints.sessions.platform, { params: { ...query } }), {
      page: query.page ?? DEFAULT_PAGE.page,
      per_page: query.per_page ?? DEFAULT_PAGE.per_page,
    }),

  revokeSession: (id: string) => api.delete<StatusResponse>(endpoints.sessions.byId(id)),

  getSessionPolicy: () => api.get<SessionPolicy>(endpoints.sessions.policy),

  saveSessionPolicy: (input: SessionPolicyInput) =>
    api.put<SessionPolicy>(endpoints.sessions.policy, { body: input }),
};
