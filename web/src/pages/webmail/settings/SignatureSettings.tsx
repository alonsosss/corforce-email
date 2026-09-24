import { useState } from 'react';
import { ERROR_CODES, errorCode, errorDetail } from '@/api/errors';
import { webmailApi, type Signature } from '@/api/webmail';
import { errorMessage } from '@/api/messages';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import { Button, Card, Checkbox, ErrorState, Skeleton, useToast } from '@/design/components';
import { t } from '@/i18n';
import { mailboxSignature } from '@/webmail/catalogs';
import { formatBytes } from '@/lib/quota';
import { utf8Length } from '../format';
import { RichEditor } from '../RichEditor';
import { htmlHasContent } from '../richText';

export function SignatureSettings() {
  const signature = useQuery((signal) => webmailApi.signature(signal), []);

  if (signature.error && !signature.data) {
    return (
      <Card title={t('webmail.signature.title')}>
        <ErrorState
          error={signature.error}
          title={t('webmail.signature.unavailable')}
          onRetry={signature.reload}
        />
      </Card>
    );
  }
  if (!signature.data) {
    return (
      <Card title={t('webmail.signature.title')}>
        <Skeleton lines={6} />
      </Card>
    );
  }
  return <SignatureForm initial={signature.data} onSaved={signature.setData} />;
}

function SignatureForm({
  initial,
  onSaved,
}: {
  initial: Signature;
  onSaved: (signature: Signature) => void;
}) {
  const toast = useToast();
  const [enabled, setEnabled] = useState(initial.enabled);
  const [onReplies, setOnReplies] = useState(initial.on_replies);
  const [html, setHtml] = useState(initial.html);
  const [problem, setProblem] = useState<string | null>(null);
  const maxBytes = initial.limits.max_html_bytes;
  const bytes = utf8Length(html);

  const save = useAction(async () => {
    let saved: Signature;
    try {
      saved = await webmailApi.setSignature({
        enabled,
        html: htmlHasContent(html) ? html : '',
        on_replies: onReplies,
      });
    } catch (err) {
      // El contenido que rechaza el directorio se senala junto al editor.
      if (errorCode(err) === ERROR_CODES.VALIDATION_ERROR && errorDetail(err, 'field') === 'html') {
        setProblem(errorMessage(err));
        return;
      }
      throw err;
    }
    // La redaccion vuelve a leer la firma guardada.
    mailboxSignature.reset();
    onSaved(saved);
    toast.success(t('webmail.signature.saved'));
  });

  const submit = () => {
    if (bytes > maxBytes) {
      setProblem(t('webmail.signature.tooLong', { max: formatBytes(maxBytes) }));
      return;
    }
    if (enabled && !htmlHasContent(html)) {
      setProblem(t('webmail.signature.empty'));
      return;
    }
    setProblem(null);
    void save.run();
  };

  return (
    <Card title={t('webmail.signature.title')}>
      <form
        className="cf-form"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <Checkbox
          label={t('webmail.signature.enabled')}
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        <div className="cf-field">
          <span className="cf-field__label" id="wm-signature-label">
            {t('webmail.signature.content')}
          </span>
          <RichEditor
            id="wm-signature"
            labelledBy="wm-signature-label"
            initialHtml={initial.html}
            allowImages
            minHeight="8rem"
            onChange={(next) => {
              setHtml(next);
              setProblem(null);
            }}
            disabled={save.busy}
          />
          {problem ? (
            <span className="cf-field__error" role="alert">
              {problem}
            </span>
          ) : (
            <span className="cf-field__hint">
              {t('webmail.signature.size', {
                used: formatBytes(bytes),
                max: formatBytes(maxBytes),
              })}
            </span>
          )}
        </div>
        <Checkbox
          label={t('webmail.signature.onReplies')}
          checked={onReplies}
          onChange={(e) => setOnReplies(e.target.checked)}
        />
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
  );
}
