import { endpoints } from './endpoints';
import { webmailRequest } from './webmail';

/*
 * Ficheros grandes por enlace (services/mail-files a traves de services/webmail, docs/adr/0014). El
 * fichero se sube con la sesion del buzon, mail-files lo analiza con ClamAV y lo guarda, y el correo
 * lleva el enlace. Topes, cuotas, caducidad y descargas los sirve el servicio en cada listado.
 */

/** Estado que ve el remitente (domain.State de mail-files). */
export const LARGE_FILE_STATES = [
  'uploading',
  'active',
  'expired',
  'exhausted',
  'revoked',
  'failed',
] as const;
export type LargeFileState = (typeof LARGE_FILE_STATES)[number];

export interface LargeFileLimits {
  max_file_bytes: number;
  default_expiry_days: number;
  max_expiry_days: number;
  default_max_downloads: number;
  max_downloads: number;
  mailbox_quota_bytes: number;
  tenant_quota_bytes: number;
  max_active_per_mailbox: number;
}

export interface LargeFileUsage {
  mailbox_bytes: number;
  mailbox_active: number;
  tenant_bytes: number;
}

export interface LargeFile {
  id: string;
  name: string;
  size_bytes: number;
  sha256: string;
  /** Solo mientras el enlace sirve (state active). */
  url: string;
  state: LargeFileState;
  expires_at: string;
  max_downloads: number;
  downloads: number;
  remaining_downloads: number;
  created_at: string;
  last_download_at: string | null;
  revoked_at: string | null;
}

/** GET /webmail/large-files. enabled false: la funcion no esta disponible y no se ofrece. */
export interface LargeFileListing {
  enabled: boolean;
  items: LargeFile[];
  usage: LargeFileUsage;
  limits: LargeFileLimits;
}

export interface LargeFileOptions {
  expiresInDays?: number;
  maxDownloads?: number;
}

const wm = endpoints.webmail;

export const largeFilesApi = {
  list: (signal?: AbortSignal) =>
    webmailRequest<LargeFileListing>('GET', wm.largeFiles, { signal }),
  /** Sube el fichero en multipart; el servicio responde cuando ClamAV lo dio por limpio y esta guardado. */
  upload: (file: File, options: LargeFileOptions = {}) => {
    const form = new FormData();
    form.append('file', file, file.name);
    return webmailRequest<LargeFile>('POST', wm.largeFiles, {
      form,
      params: { expires_in_days: options.expiresInDays, max_downloads: options.maxDownloads },
    });
  },
  revoke: (id: string) => webmailRequest<LargeFile>('DELETE', wm.largeFile(id)),
};
