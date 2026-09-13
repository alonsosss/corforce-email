/**
 * Copia al portapapeles. Devuelve false si el navegador no lo permite (contexto no seguro,
 * permiso denegado): quien llama avisa al usuario en lugar de fingir que se copio.
 */
export async function copyToClipboard(text: string): Promise<boolean> {
  try {
    if (!navigator.clipboard?.writeText) return false;
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}
