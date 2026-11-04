// Латинские якоря заголовков вместо кириллических.
//
// По умолчанию якорь — это заголовок как есть («#как-вывести-бота-наружу»), и
// в ссылке, скопированной из браузера, он превращается в длинную кашу из
// %D0%BA%D0%B0…. Здесь якорь строится транслитом того же заголовка.
//
// Старые ссылки с кириллическим якорем продолжают работать: скрипт на
// странице прогоняет такой якорь через ту же функцию и переходит к заголовку.

// Одна функция и для сборки, и для страницы: в <head> уходит её исходный текст,
// поэтому она не должна ссылаться ни на что снаружи.
export function latinSlug(s) {
  var map = {
    а: 'a', б: 'b', в: 'v', г: 'g', д: 'd', е: 'e', ё: 'e', ж: 'zh', з: 'z', и: 'i', й: 'y',
    к: 'k', л: 'l', м: 'm', н: 'n', о: 'o', п: 'p', р: 'r', с: 's', т: 't', у: 'u', ф: 'f',
    х: 'kh', ц: 'ts', ч: 'ch', ш: 'sh', щ: 'shch', ъ: '', ы: 'y', ь: '', э: 'e', ю: 'yu', я: 'ya',
  };
  var out = '';
  var low = String(s).toLowerCase();
  for (var i = 0; i < low.length; i++) {
    var c = low[i];
    out += Object.prototype.hasOwnProperty.call(map, c) ? map[c] : c;
  }
  return out
    .replace(/\s+/g, '-')
    .replace(/[^a-z0-9_-]/g, '')
    .replace(/-+/g, '-')
    .replace(/^-|-$/g, '');
}

function headingIdsPlugin() {
  var seen = new Map();
  return {
    name: 'latin-heading-ids',
    element: {
      filter: ['h1', 'h2', 'h3', 'h4', 'h5', 'h6'],
      visit(node, ctx) {
        if (typeof node.properties?.id === 'string') return;
        var base = latinSlug(ctx.textContent(node)) || 'section';
        var n = seen.get(base) || 0;
        seen.set(base, n + 1);
        ctx.setProperty(node, 'id', n === 0 ? base : base + '-' + n);
      },
    },
  };
}

// Старый кириллический якорь → новый латинский.
export const legacyAnchorScript =
  '(function(){var latinSlug=' +
  latinSlug.toString() +
  ';function go(){var h;try{h=decodeURIComponent(location.hash.slice(1));}catch(e){return;}' +
  'if(!h||!/[^\\x00-\\x7f]/.test(h)||document.getElementById(h))return;' +
  'var id=latinSlug(h);if(id&&document.getElementById(id))location.replace("#"+id);}' +
  'if(document.readyState==="loading")document.addEventListener("DOMContentLoaded",go);else go();' +
  'window.addEventListener("hashchange",go);})();';

// Интеграция ставит плагин в начало конвейера markdown, раньше Starlight и
// Astro: их собственные плагины заголовков готовый id не трогают.
export function latinHeadingIds() {
  return {
    name: 'latin-heading-ids',
    hooks: {
      'astro:config:setup': ({ config, logger }) => {
        var proc = config.markdown && config.markdown.processor;
        if (!proc || proc.name !== 'satteri' || !proc.options || !Array.isArray(proc.options.hastPlugins)) {
          throw new Error('latin-heading-ids: ожидался markdown-процессор satteri');
        }
        proc.options.hastPlugins.unshift(function () {
          return headingIdsPlugin();
        });
        logger.debug('латинские якоря заголовков включены');
      },
    },
  };
}
