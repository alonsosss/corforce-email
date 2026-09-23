// mjml-browser no publica tipos. Solo se declara lo que usa pages/templates/editor/mjml.ts:
// la firma sincrona de mjml 4 (la 5 devuelve una promesa y cambia el contrato).
declare module 'mjml-browser' {
  export interface MjmlError {
    line: number;
    message: string;
    tagName: string;
    formattedMessage: string;
  }

  export interface MjmlOptions {
    /** Fuentes que se enlazan desde Google Fonts si el correo las usa; {} no enlaza ninguna. */
    fonts?: Record<string, string>;
    keepComments?: boolean;
    validationLevel?: 'strict' | 'soft' | 'skip';
    minify?: boolean;
    beautify?: boolean;
  }

  export interface MjmlResult {
    html: string;
    errors: MjmlError[];
  }

  export default function mjml2html(mjml: string, options?: MjmlOptions): MjmlResult;
}

// Traducciones de GrapesJS y de grapesjs-mjml: objetos de mensajes sin tipos propios.
declare module 'grapesjs/locale/es.mjs' {
  const messages: Record<string, unknown>;
  export default messages;
}

declare module 'grapesjs-mjml/locale/es' {
  const messages: Record<string, unknown>;
  export default messages;
}
