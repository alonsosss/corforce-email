export interface HtmlPreviewFrameProps {
  html: string;
  title: string;
  height?: number;
}

/**
 * Unico sitio donde se pinta HTML que no es de la aplicacion (plantillas, pies de pagina,
 * avisos): un iframe con sandbox vacio, sin scripts, formularios, ventanas, navegacion ni
 * mismo origen, y el documento en srcdoc. Ese HTML nunca entra en el DOM de la pagina.
 */
export function HtmlPreviewFrame({ html, title, height = 360 }: HtmlPreviewFrameProps) {
  return (
    <iframe
      className="cf-preview-frame"
      title={title}
      sandbox=""
      referrerPolicy="no-referrer"
      srcDoc={html}
      style={{ height }}
    />
  );
}
