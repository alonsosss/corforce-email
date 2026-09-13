/**
 * Entrega al usuario un fichero ya en memoria como descarga. El tipo se fuerza a
 * application/octet-stream: el navegador nunca lo abre ni lo interpreta, solo lo guarda.
 */
export function saveBlob(blob: Blob, filename: string): void {
  const safe = new Blob([blob], { type: 'application/octet-stream' });
  const url = URL.createObjectURL(safe);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  link.rel = 'noopener';
  link.click();
  // Revocar en el mismo tick puede cortar la descarga de un fichero grande en algunos
  // navegadores; la URL solo existe dentro de esta pagina.
  window.setTimeout(() => URL.revokeObjectURL(url), 0);
}
