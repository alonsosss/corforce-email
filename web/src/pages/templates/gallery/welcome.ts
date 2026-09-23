import type { BrandTokens } from '../editor/brand';
import {
  cta,
  defaultHref,
  emailDocument,
  greeting,
  header,
  heading,
  mjColumn,
  mjSection,
  paragraph,
} from './layout';

function feature(brand: BrandTokens, title: string, body: string): string {
  return mjColumn(
    heading(brand, title, 'font-size="17px" padding="10px 20px 4px"') +
      paragraph(brand, body, 'font-size="14px" padding="0 20px 10px"'),
  );
}

export function welcomeTemplate(brand: BrandTokens): string {
  return emailDocument(brand, {
    preheader: 'Gracias por unirte. Esto es lo que puedes esperar a partir de hoy.',
    sections: [
      header(brand),
      mjSection(
        brand,
        mjColumn(
          heading(brand, greeting('Te damos la bienvenida')) +
            paragraph(
              brand,
              'Nos alegra que formes parte de nuestra comunidad. A partir de ahora recibirás ' +
                'novedades, recursos útiles y beneficios pensados para ti. Queremos que cada ' +
                'mensaje te aporte algo, por eso solo te escribiremos cuando tengamos algo que valga la pena contarte.',
            ) +
            cta(brand, 'Conoce más sobre nosotros', defaultHref(brand)),
        ),
      ),
      mjSection(
        brand,
        feature(
          brand,
          'Novedades',
          'Sé de los primeros en conocer nuestros lanzamientos y mejoras.',
        ) +
          feature(
            brand,
            'Recursos',
            'Guías y consejos prácticos para sacar el máximo partido a lo que ofrecemos.',
          ) +
          feature(
            brand,
            'Beneficios',
            'Condiciones especiales reservadas para quienes reciben nuestros correos.',
          ),
        'padding="0 0 20px"',
      ),
      mjSection(
        brand,
        mjColumn(
          paragraph(
            brand,
            'Si tienes cualquier pregunta, responde a este correo y te ayudaremos con gusto.',
            `font-size="14px" color="${brand.muted}"`,
          ),
        ),
        'padding="0 0 20px"',
      ),
    ],
  });
}
