// Значки пунктов меню.
//
// У пункта меню в astro.config.mjs стоит attrs: icon('имя') — на ссылку попадает
// data-icon="имя", а значок рисуется из этой таблицы через CSS-маску, поэтому
// сам красится в цвет текста и не требует картинок.
//
// Значок — содержимое SVG 24×24, только линии (обводка задаётся ниже).
export const icons = {
  book: '<path d="M12 7v13"/><path d="M3 5h5.5A3.5 3.5 0 0 1 12 8.5 3.5 3.5 0 0 1 15.5 5H21v13h-6a3 3 0 0 0-3 2 3 3 0 0 0-3-2H3z"/>',
  checklist:
    '<path d="M3 6l1.5 1.5L7 5"/><path d="M3 12l1.5 1.5L7 11"/><path d="M3 18l1.5 1.5L7 17"/><path d="M11 6h10M11 12h10M11 18h10"/>',
  cpu: '<rect x="5" y="5" width="14" height="14" rx="2"/><rect x="9.5" y="9.5" width="5" height="5" rx="1"/><path d="M9 2v3M15 2v3M9 19v3M15 19v3M2 9h3M2 15h3M19 9h3M19 15h3"/>',
  download: '<path d="M12 3v12"/><path d="M7 11l5 5 5-5"/><path d="M4 20h16"/>',
  wand: '<path d="M4 20L15.5 8.5"/><path d="M15 3v3M13.5 4.5h3M20 9v3M18.5 10.5h3M8 4v2M7 5h2"/>',
  check: '<circle cx="12" cy="12" r="9"/><path d="M8 12.5l3 3 5-6"/>',
  grid: '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>',
  cart: '<circle cx="9" cy="20" r="1.5"/><circle cx="18" cy="20" r="1.5"/><path d="M2 3h3l2.5 12h11L21 7H6"/>',
  megaphone:
    '<path d="M3 10v4a1 1 0 0 0 1 1h3l8 4V5L7 9H4a1 1 0 0 0-1 1z"/><path d="M19 9a4 4 0 0 1 0 6"/><path d="M8 15.5V19a1 1 0 0 0 1 1h1.5"/>',
  layout: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 9h18M9 9v11"/>',
  text: '<path d="M4 7V4h16v3"/><path d="M12 4v16"/><path d="M9 20h6"/>',
  users:
    '<circle cx="9" cy="8" r="3.5"/><path d="M2.5 20a6.5 6.5 0 0 1 13 0"/><path d="M16 4.6a3.5 3.5 0 0 1 0 6.8"/><path d="M18 14.5a6.5 6.5 0 0 1 3.5 5.5"/>',
  cog: '<circle cx="12" cy="12" r="3"/><circle cx="12" cy="12" r="7.5"/><path d="M12 2v2.5M12 19.5V22M2 12h2.5M19.5 12H22M4.9 4.9l1.8 1.8M17.3 17.3l1.8 1.8M4.9 19.1l1.8-1.8M17.3 6.7l1.8-1.8"/>',
  image: '<rect x="3" y="4" width="18" height="16" rx="2"/><circle cx="8.5" cy="9.5" r="1.5"/><path d="M21 16l-5-5-9 9"/>',
  globe: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18"/><path d="M12 3a14 14 0 0 1 0 18 14 14 0 0 1 0-18z"/>',
  star: '<path d="M12 3l2.7 5.6 6.1.8-4.5 4.3 1.1 6.1L12 16.9l-5.4 2.9 1.1-6.1-4.5-4.3 6.1-.8z"/>',
  wallet: '<rect x="3" y="6" width="18" height="14" rx="2"/><path d="M3 10.5h18"/><path d="M15.5 15.5h2"/><path d="M6.5 6l8.5-3 1 3"/>',
  refresh:
    '<path d="M20 11a8 8 0 0 0-14.5-4L3 9"/><path d="M3 4v5h5"/><path d="M4 13a8 8 0 0 0 14.5 4L21 15"/><path d="M21 20v-5h-5"/>',
  branch:
    '<circle cx="6" cy="5" r="2.5"/><circle cx="6" cy="19" r="2.5"/><circle cx="18" cy="8" r="2.5"/><path d="M6 7.5v9"/><path d="M18 10.5c0 3.5-3 5-9.6 6"/>',
  lifebuoy:
    '<circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="3.5"/><path d="M5.6 5.6l3.9 3.9M14.5 14.5l3.9 3.9M18.4 5.6l-3.9 3.9M9.5 14.5l-3.9 3.9"/>',
  help: '<circle cx="12" cy="12" r="9"/><path d="M9.5 9.5a2.5 2.5 0 1 1 3.6 2.2c-.7.4-1.1 1-1.1 1.8"/><path d="M12 17h.01"/>',
  terminal: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M7 9l3 3-3 3"/><path d="M13 15h4"/>',
  sliders:
    '<path d="M4 6h10M18 6h2M4 12h2M10 12h10M4 18h8M16 18h4"/><circle cx="16" cy="6" r="2"/><circle cx="8" cy="12" r="2"/><circle cx="14" cy="18" r="2"/>',
  key: '<circle cx="8" cy="15" r="4"/><path d="M10.8 12.2L20 3"/><path d="M17 6l3 3"/><path d="M14 9l2.5 2.5"/>',
  file: '<path d="M6 3h8l5 5v13H6z"/><path d="M14 3v5h5"/><path d="M9 13h7M9 17h7"/>',
};

/** attrs для пункта меню: { label, slug, attrs: icon('book') }. */
export function icon(name) {
  if (!icons[name]) throw new Error(`нет значка «${name}» в src/icons.mjs`);
  return { 'data-icon': name };
}

function dataUri(body) {
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="black" ` +
    `stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">${body}</svg>`;
  return `url("data:image/svg+xml,${encodeURIComponent(svg)}")`;
}

/** CSS со всеми значками — уходит в <head> одним <style>. */
export const iconCss = Object.entries(icons)
  .map(([name, body]) => `[data-icon='${name}']{--docs-icon:${dataUri(body)}}`)
  .join('');
