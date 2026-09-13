import { useEffect, useState, type FormEvent } from 'react';
import { identityApi } from '@/api/identity';
import { errorMessage } from '@/api/messages';
import { useAuth } from '@/auth/useAuth';
import { useAction } from '@/hooks/useAction';
import {
  Badge,
  Button,
  Card,
  DescriptionList,
  FormField,
  Input,
  Skeleton,
  useToast,
} from '@/design/components';
import { formatDateTime } from '@/lib/format';
import { rules, validateField, type FieldErrors } from '@/lib/validate';
import { t, tEnum } from '@/i18n';

type Field = 'first_name' | 'last_name';

export function ProfileForm() {
  const { user, userId, setUser } = useAuth();
  const toast = useToast();
  const [firstName, setFirstName] = useState('');
  const [lastName, setLastName] = useState('');
  const [errors, setErrors] = useState<FieldErrors<Field>>({});

  useEffect(() => {
    if (user) {
      setFirstName(user.first_name);
      setLastName(user.last_name);
    }
  }, [user]);

  const save = useAction(async () => {
    if (!userId) return;
    const { data } = await identityApi.updateUser(userId, {
      first_name: firstName.trim(),
      last_name: lastName.trim(),
    });
    setUser(data);
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const next: FieldErrors<Field> = {
      first_name: validateField(firstName, rules.required, rules.maxLength(100)) ?? undefined,
      last_name: validateField(lastName, rules.required, rules.maxLength(100)) ?? undefined,
    };
    setErrors(next);
    if (next.first_name || next.last_name) return;
    if (await save.run()) toast.success(t('account.profile.saved'));
  };

  if (!user) {
    return (
      <Card>
        <Skeleton lines={4} />
      </Card>
    );
  }

  return (
    <div className="cf-stack">
      <Card title={t('account.tab.profile')}>
        <form className="cf-form" onSubmit={(e) => void submit(e)} noValidate>
          <div className="cf-form__row">
            <FormField
              label={t('account.profile.firstName')}
              htmlFor="profile-first"
              required
              error={errors.first_name}
            >
              <Input
                id="profile-first"
                value={firstName}
                onChange={(e) => setFirstName(e.target.value)}
                invalid={Boolean(errors.first_name)}
                autoComplete="given-name"
              />
            </FormField>
            <FormField
              label={t('account.profile.lastName')}
              htmlFor="profile-last"
              required
              error={errors.last_name}
            >
              <Input
                id="profile-last"
                value={lastName}
                onChange={(e) => setLastName(e.target.value)}
                invalid={Boolean(errors.last_name)}
                autoComplete="family-name"
              />
            </FormField>
          </div>
          <FormField label={t('common.email')} htmlFor="profile-email">
            <Input id="profile-email" value={user.email} disabled />
          </FormField>
          {save.error ? (
            <div className="cf-form__error" role="alert">
              {errorMessage(save.error)}
            </div>
          ) : null}
          <div className="cf-form__actions">
            <Button type="submit" variant="primary" loading={save.busy}>
              {t('common.save')}
            </Button>
          </div>
        </form>
      </Card>
      <Card>
        <DescriptionList
          items={[
            { label: t('common.status'), value: tEnum('users.status', user.status) },
            {
              label: t('account.profile.mfaStatus'),
              value: user.mfa_enabled ? (
                <Badge tone="success">{t('common.enabled')}</Badge>
              ) : (
                <Badge>{t('common.disabled')}</Badge>
              ),
            },
            { label: t('account.profile.lastLogin'), value: formatDateTime(user.last_login_at) },
            { label: t('common.createdAt'), value: formatDateTime(user.created_at) },
          ]}
        />
      </Card>
    </div>
  );
}
