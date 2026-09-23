import mjml2html from 'mjml-browser';

// Compilacion MJML -> HTML en el navegador (ADR 0012): el servidor recibe el HTML ya
// compilado y solo lo valida. Todo el editor compila por aqui, tambien el lienzo, para que
// lo que se ve y lo que se guarda salgan del mismo compilador y con las mismas opciones.

export interface CompileResult {
  html: string;
  /** Avisos del validador de MJML: el HTML se genera igual, pero conviene revisarlos. */
  errors: string[];
}

/**
 * fonts vacio: MJML enlaza Google Fonts cuando el correo usa sus fuentes por defecto, y un
 * <link rel=stylesheet> ni lo respetan los clientes ni lo admite el verificador
 * (external_stylesheet). Las fuentes son las del kit de marca, con alternativa segura.
 */
export const MJML_OPTIONS = {
  fonts: {},
  keepComments: false,
  validationLevel: 'soft',
} as const;

export function compileMjml(source: string): CompileResult {
  const result = mjml2html(source, { ...MJML_OPTIONS });
  return {
    html: result.html,
    errors: result.errors.map((e) => e.formattedMessage || e.message),
  };
}
