import { Button } from './Button';
import { useToast } from './Toast';
import { IconCopy } from '../icons';
import { copyToClipboard } from '@/lib/clipboard';
import { t } from '@/i18n';

export interface CopyButtonProps {
  value: string;
  label?: string;
  /** Muestra el texto junto al icono. */
  withText?: boolean;
}

export function CopyButton({ value, label, withText = false }: CopyButtonProps) {
  const toast = useToast();
  const text = label ?? t('common.copy');
  const copy = async () => {
    if (await copyToClipboard(value)) toast.success(t('common.copied'));
    else toast.error(t('common.copyFailed'));
  };
  return (
    <Button
      size="sm"
      variant="ghost"
      iconOnly={!withText}
      icon={<IconCopy size={14} />}
      title={text}
      onClick={() => void copy()}
    >
      {text}
    </Button>
  );
}
