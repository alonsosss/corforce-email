import { t } from '@/i18n';

/*
 * Avisos de escritorio de correo nuevo. Son opcionales: el permiso del navegador solo se
 * pide cuando el usuario pulsa el boton, y la preferencia vive en este navegador. El aviso
 * no lleva remitente ni asunto: solo cuantos mensajes nuevos hay, porque el escritorio lo
 * puede ver cualquiera que pase delante de la pantalla.
 */

const PREFERENCE_KEY = 'cf.webmail.desktopNotifications';
const TAG = 'cf-webmail-inbox';

export function notificationsSupported(): boolean {
  return typeof window !== 'undefined' && 'Notification' in window;
}

export function notificationPermission(): NotificationPermission | 'unsupported' {
  return notificationsSupported() ? Notification.permission : 'unsupported';
}

export function readNotifyPreference(): boolean {
  try {
    return window.localStorage.getItem(PREFERENCE_KEY) === '1';
  } catch {
    return false;
  }
}

export function writeNotifyPreference(enabled: boolean): void {
  try {
    if (enabled) window.localStorage.setItem(PREFERENCE_KEY, '1');
    else window.localStorage.removeItem(PREFERENCE_KEY);
  } catch {
    // Almacenamiento bloqueado: la preferencia dura lo que la pestana.
  }
}

/** Pide permiso si hace falta; true si se puede avisar. */
export async function requestNotifyPermission(): Promise<boolean> {
  if (!notificationsSupported()) return false;
  if (Notification.permission === 'granted') return true;
  if (Notification.permission === 'denied') return false;
  return (await Notification.requestPermission()) === 'granted';
}

/** Mensajes nuevos: los no leidos que subieron desde el ultimo recuento. */
export function newUnread(before: number | null, after: number | null): number {
  if (before === null || after === null) return 0;
  return Math.max(0, after - before);
}

export function notifyNewMail(count: number, onClick: () => void): void {
  if (count <= 0 || notificationPermission() !== 'granted') return;
  try {
    const notification = new Notification(t('webmail.notify.title'), {
      body: t(count === 1 ? 'webmail.notify.one' : 'webmail.notify.many', { n: count }),
      tag: TAG,
    });
    notification.onclick = () => {
      window.focus();
      onClick();
      notification.close();
    };
  } catch {
    // Algunos navegadores moviles solo avisan desde un service worker: no se avisa.
  }
}
