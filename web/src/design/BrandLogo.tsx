import { useId } from 'react';

/**
 * Isotipo del producto: hexagono abierto con la F. Los degradados llevan un id por
 * instancia porque la pantalla de acceso lo pinta dos veces.
 */
export function BrandLogo({ size = 48 }: { size?: number }) {
  const id = useId();
  const shell = `${id}-shell`;
  const letter = `${id}-letter`;
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 48 48"
      fill="none"
      aria-hidden="true"
      focusable="false"
      className="cf-brand-logo"
    >
      <defs>
        <linearGradient id={shell} x1="6" y1="6" x2="30" y2="44" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="#2c5aa0" />
          <stop offset="1" stopColor="#15305c" />
        </linearGradient>
        <linearGradient id={letter} x1="18" y1="12" x2="44" y2="34" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="#3b82f6" />
          <stop offset="1" stopColor="#7cc4fa" />
        </linearGradient>
      </defs>
      <path
        d="M24 3 42.2 13.5 36 17.1 24 10.2 12.2 17v14l11.8 6.8V45L6 34.5v-21z"
        fill={`url(#${shell})`}
      />
      <path
        d="M18.5 16.5h25l-4.6 6.2H24.7v3.6h12.1l-4.4 6H24.7V41l-6.2-3.6z"
        fill={`url(#${letter})`}
      />
    </svg>
  );
}
