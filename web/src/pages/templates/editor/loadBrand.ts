import { errorMessage } from '@/api/messages';
import { templatesApi } from '@/api/templates';
import { t } from '@/i18n';
import { findAsset } from '../assets';
import type { BrandContext } from './session';

/** Kit de marca y logo para el editor; sin permiso o sin respuesta, el motivo. */
export async function loadBrand(
  canReadKit: boolean,
  canReadAssets: boolean,
): Promise<BrandContext> {
  if (!canReadKit) {
    return { kit: null, logo: null, unavailable: t('templates.brandKit.noPermission') };
  }
  try {
    const kit = await templatesApi.brandKit();
    const logo = kit.logo_asset_id && canReadAssets ? await findAsset(kit.logo_asset_id) : null;
    return { kit, logo, unavailable: null };
  } catch (err) {
    return { kit: null, logo: null, unavailable: errorMessage(err) };
  }
}
