import { useMemo } from 'react';
import { create as createQr } from 'qrcode';

export interface QrCodeProps {
  value: string;
  label: string;
}

/**
 * QR renderizado como SVG a partir de la matriz de modulos: sin canvas, sin innerHTML y
 * sin imagenes externas, compatible con la CSP del gateway.
 */
export function QrCode({ value, label }: QrCodeProps) {
  const matrix = useMemo(() => {
    const qr = createQr(value, { errorCorrectionLevel: 'M' });
    const { size, data } = qr.modules;
    const cells: string[] = [];
    for (let y = 0; y < size; y++) {
      for (let x = 0; x < size; x++) {
        if (data[y * size + x]) cells.push(`M${x} ${y}h1v1h-1z`);
      }
    }
    return { size, path: cells.join('') };
  }, [value]);

  return (
    <svg
      className="cf-qr"
      viewBox={`0 0 ${matrix.size} ${matrix.size}`}
      role="img"
      aria-label={label}
      shapeRendering="crispEdges"
    >
      <rect width={matrix.size} height={matrix.size} fill="#ffffff" />
      <path d={matrix.path} fill="#000000" />
    </svg>
  );
}
