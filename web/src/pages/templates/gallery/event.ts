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

function detail(brand: BrandTokens, label: string, value: string): string {
  return mjColumn(
    paragraph(
      brand,
      `<strong>${label}</strong><br/>${value}`,
      'align="center" font-size="14px" padding="6px 10px"',
    ),
  );
}

export function eventTemplate(brand: BrandTokens): string {
  return emailDocument(brand, {
    preheader: 'Reserva tu plaza: te contamos la fecha, el lugar y el programa.',
    sections: [
      header(brand),
      mjSection(
        brand,
        mjColumn(
          heading(brand, 'Te invitamos a nuestro próximo evento', 'align="center"') +
            paragraph(brand, greeting('Hola'), 'align="center"') +
            paragraph(
              brand,
              'Nos encantaría contar contigo en una jornada pensada para compartir novedades, ' +
                'resolver dudas y conocer a otras personas de la comunidad. Las plazas son ' +
                'limitadas, así que te recomendamos reservar la tuya cuanto antes.',
              'align="center"',
            ),
        ),
      ),
      mjSection(
        brand,
        detail(brand, 'Fecha', 'Indica aquí el día') +
          detail(brand, 'Hora', 'Indica aquí la hora') +
          detail(brand, 'Lugar', 'Indica aquí el lugar o el enlace'),
        `background-color="${brand.background}" padding="12px 0"`,
      ),
      mjSection(
        brand,
        mjColumn(
          cta(brand, 'Reservar mi plaza', defaultHref(brand)) +
            paragraph(
              brand,
              'Si finalmente no puedes asistir, avísanos para liberar tu plaza.',
              `align="center" font-size="13px" color="${brand.muted}"`,
            ),
        ),
      ),
    ],
  });
}
