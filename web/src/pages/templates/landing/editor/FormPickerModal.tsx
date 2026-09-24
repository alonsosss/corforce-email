import { useState } from 'react';
import type { SubscriptionForm } from '@/api/forms';
import { Alert, Button, FormField, Modal, Select } from '@/design/components';
import { t, tEnum } from '@/i18n';

export interface FormPickerModalProps {
  forms: readonly SubscriptionForm[];
  onPick: (form: SubscriptionForm) => void;
  onClose: () => void;
}

/** Elige el formulario de suscripcion que se incrusta en la pagina. */
export function FormPickerModal({ forms, onPick, onClose }: FormPickerModalProps) {
  const [id, setId] = useState('');
  const [error, setError] = useState<string | null>(null);
  const chosen = forms.find((f) => f.id === id) ?? null;

  const insert = () => {
    if (!chosen) {
      setError(t('validation.required'));
      return;
    }
    onPick(chosen);
  };

  return (
    <Modal
      open
      title={t('templates.pages.editor.formPickerTitle')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button variant="primary" disabled={forms.length === 0} onClick={insert}>
            {t('templates.pages.editor.insertForm')}
          </Button>
        </>
      }
    >
      <div className="cf-stack" style={{ gap: 'var(--cf-space-3)' }}>
        <span className="cf-text-sm cf-text-secondary">
          {t('templates.pages.editor.formPickerHint')}
        </span>
        {forms.length === 0 ? (
          <Alert tone="info">{t('templates.pages.editor.noForms')}</Alert>
        ) : (
          <FormField
            label={t('templates.pages.editor.form')}
            htmlFor="page-form-picker"
            required
            error={error}
          >
            <Select
              id="page-form-picker"
              placeholder={t('common.select')}
              options={forms.map((f) => ({
                value: f.id,
                label:
                  f.status === 'active'
                    ? f.name
                    : `${f.name} (${tEnum('contacts.forms.status', f.status)})`,
              }))}
              value={id}
              onChange={(e) => {
                setId(e.target.value);
                setError(null);
              }}
              invalid={Boolean(error)}
            />
          </FormField>
        )}
        {chosen && chosen.status !== 'active' ? (
          <Alert tone="warning">{t('templates.pages.editor.formDisabled')}</Alert>
        ) : null}
      </div>
    </Modal>
  );
}
