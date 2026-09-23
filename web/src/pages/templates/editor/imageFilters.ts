// Filtros del editor de imagenes. La vista previa usa el filtro CSS equivalente y la imagen
// que se guarda se calcula pixel a pixel con las mismas matrices de la especificacion
// Filter Effects, porque CanvasRenderingContext2D.filter no esta en todos los navegadores.

export type ImageFilter = 'none' | 'grayscale' | 'sepia' | 'brighten' | 'contrast';

export const IMAGE_FILTERS: readonly ImageFilter[] = [
  'none',
  'grayscale',
  'sepia',
  'brighten',
  'contrast',
];

const BRIGHTNESS = 1.15;
const CONTRAST = 1.2;

export function cssFilter(filter: ImageFilter): string {
  switch (filter) {
    case 'grayscale':
      return 'grayscale(1)';
    case 'sepia':
      return 'sepia(1)';
    case 'brighten':
      return `brightness(${BRIGHTNESS})`;
    case 'contrast':
      return `contrast(${CONTRAST})`;
    case 'none':
      return 'none';
  }
}

type Matrix = readonly [number, number, number, number, number, number, number, number, number];

const GRAYSCALE: Matrix = [0.2126, 0.7152, 0.0722, 0.2126, 0.7152, 0.0722, 0.2126, 0.7152, 0.0722];
const SEPIA: Matrix = [0.393, 0.769, 0.189, 0.349, 0.686, 0.168, 0.272, 0.534, 0.131];

function clamp(value: number): number {
  return value < 0 ? 0 : value > 255 ? 255 : Math.round(value);
}

function applyMatrix(data: Uint8ClampedArray, m: Matrix): void {
  for (let i = 0; i < data.length; i += 4) {
    const r = data[i] ?? 0;
    const g = data[i + 1] ?? 0;
    const b = data[i + 2] ?? 0;
    data[i] = clamp(m[0] * r + m[1] * g + m[2] * b);
    data[i + 1] = clamp(m[3] * r + m[4] * g + m[5] * b);
    data[i + 2] = clamp(m[6] * r + m[7] * g + m[8] * b);
  }
}

function applyLinear(data: Uint8ClampedArray, slope: number, intercept: number): void {
  for (let i = 0; i < data.length; i += 4) {
    data[i] = clamp((data[i] ?? 0) * slope + intercept);
    data[i + 1] = clamp((data[i + 1] ?? 0) * slope + intercept);
    data[i + 2] = clamp((data[i + 2] ?? 0) * slope + intercept);
  }
}

/** Aplica el filtro sobre los pixeles RGBA; el canal alfa no cambia. */
export function applyFilter(data: Uint8ClampedArray, filter: ImageFilter): void {
  switch (filter) {
    case 'grayscale':
      applyMatrix(data, GRAYSCALE);
      return;
    case 'sepia':
      applyMatrix(data, SEPIA);
      return;
    case 'brighten':
      applyLinear(data, BRIGHTNESS, 0);
      return;
    case 'contrast':
      applyLinear(data, CONTRAST, 127.5 * (1 - CONTRAST));
      return;
    case 'none':
      return;
  }
}
