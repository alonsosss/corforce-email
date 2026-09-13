import { Badge, Checkbox } from '@/design/components';
import { tEnum } from '@/i18n';

// Accesos por protocolo de un buzon y de sus contrasenas de aplicacion (mail-directory).
export const MAILBOX_ACCESS_KEYS = [
  'imap_access',
  'pop3_access',
  'smtp_access',
  'sieve_access',
] as const;
export const APP_PASSWORD_ACCESS_KEYS = [...MAILBOX_ACCESS_KEYS, 'dav_access'] as const;

export type AccessKey = (typeof APP_PASSWORD_ACCESS_KEYS)[number];

export function allAccess<K extends AccessKey>(
  keys: readonly K[],
  value: boolean,
): Record<K, boolean> {
  const out = {} as Record<K, boolean>;
  for (const key of keys) out[key] = value;
  return out;
}

export function pickAccess<K extends AccessKey>(
  keys: readonly K[],
  source: Record<K, boolean>,
): Record<K, boolean> {
  const out = {} as Record<K, boolean>;
  for (const key of keys) out[key] = source[key];
  return out;
}

export interface AccessCheckboxesProps<K extends AccessKey> {
  keys: readonly K[];
  value: Record<K, boolean>;
  onChange: (next: Record<K, boolean>) => void;
  disabled?: boolean;
}

export function AccessCheckboxes<K extends AccessKey>({
  keys,
  value,
  onChange,
  disabled,
}: AccessCheckboxesProps<K>) {
  return (
    <div className="cf-inline-list" style={{ gap: 'var(--cf-space-4)' }}>
      {keys.map((key) => (
        <Checkbox
          key={key}
          label={tEnum('mail.protocol', key)}
          checked={value[key]}
          disabled={disabled}
          onChange={(e) => onChange({ ...value, [key]: e.target.checked })}
        />
      ))}
    </div>
  );
}

export function ProtocolBadges({ value }: { value: Partial<Record<AccessKey, boolean>> }) {
  const enabled = APP_PASSWORD_ACCESS_KEYS.filter((key) => value[key]);
  return (
    <span className="cf-inline-list">
      {enabled.map((key) => (
        <Badge key={key} tone="info">
          {tEnum('mail.protocol', key)}
        </Badge>
      ))}
    </span>
  );
}
