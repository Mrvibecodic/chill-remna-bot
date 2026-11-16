// Проверка после сборки: оформление сайта опирается на разметку Starlight
// (шапка переезжает в колонку меню, меню и оглавление стоят по своим местам).
// Если новая версия Starlight переименует эти части, сборка упадёт здесь, а
// не выйдет сайт со съехавшей раскладкой.
import { readFileSync } from 'node:fs';

const page = readFileSync(new URL('../dist/start/install/index.html', import.meta.url), 'utf8');
const hooks = {
  'шапка': /<header class="header[ "]/,
  'название сайта': /class="title-wrapper[ "]/,
  'поиск': /<site-search[ >]/,
  'ссылки и переключатели': /class="[^"]*\bright-group\b/,
  'панель меню': /class="sidebar-pane[ "]/,
  'содержимое меню': /class="sidebar-content[ "]/,
  'группа меню': /<summary[ >]/,
  'пункт меню со значком': /<a [^>]*data-icon="/,
  'основная колонка': /class="main-frame[ "]/,
  'текст страницы': /class="[^"]*\bsl-markdown-content\b/,
  'оглавление справа': /class="right-sidebar-container[ "]/,
  'путь над заголовком': /class="docs-crumbs"/,
  'переключатель дизайна': /<docs-design-select[ >]/,
};
const missing = Object.entries(hooks)
  .filter(([, re]) => !re.test(page))
  .map(([name]) => name);
if (missing.length) {
  console.error('Разметка Starlight изменилась, оформление нужно сверить (src/styles/theme.css): ' + missing.join(', '));
  process.exit(1);
}
console.log('разметка Starlight на месте: ' + Object.keys(hooks).length + ' частей');
