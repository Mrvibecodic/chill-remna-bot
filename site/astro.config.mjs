// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import { latinHeadingIds, legacyAnchorScript } from './src/heading-ids.mjs';
import { icon, iconCss } from './src/icons.mjs';

// Сайт документации chill-remna-bot. Собирается в статику и выкладывается на
// GitHub Pages (домен remna.shop), поэтому никакого рантайма на сервере нет.
export default defineConfig({
  site: 'https://remna.shop',
  trailingSlash: 'ignore',
  build: { format: 'directory' },
  integrations: [
    latinHeadingIds(),
    starlight({
      title: 'Chill Remna bot',
      description:
        'Телеграм-бот «магазин + админка» для панели Remnawave: тарифы, оплаты, триал, рефералка, мини-апп и веб-кабинет.',
      defaultLocale: 'root',
      locales: { root: { label: 'Русский', lang: 'ru' } },
      customCss: ['@fontsource-variable/onest', './src/styles/theme.css'],
      components: {
        // Название со значком, путь и описание над заголовком, переключатели
        // дизайна и цветовой схемы.
        SiteTitle: './src/components/SiteTitle.astro',
        PageTitle: './src/components/PageTitle.astro',
        ThemeSelect: './src/components/ThemeSelect.astro',
      },
      // Длинные команды (curl с URL) переносим по словам, чтобы блок кода не
      // уезжал в горизонтальный скролл — особенно на телефоне.
      expressiveCode: {
        defaultProps: { wrap: true, preserveIndent: true },
        styleOverrides: { borderRadius: '12px' },
      },
      social: [
        { icon: 'telegram', label: 'Группа в Telegram', href: 'https://t.me/+2lmP1yhxpCE3MDcy' },
        { icon: 'comment-alt', label: 'Чат в Telegram', href: 'https://t.me/+8BJQXYXYLqM4YWYy' },
        { icon: 'github', label: 'GitHub', href: 'https://github.com/Mrvibecodic/chill-remna-bot' },
      ],
      editLink: {
        baseUrl: 'https://github.com/Mrvibecodic/chill-remna-bot/edit/main/site/',
      },
      lastUpdated: true,
      favicon: '/favicon.svg',
      head: [
        { tag: 'link', attrs: { rel: 'icon', href: '/favicon-32.png', sizes: '32x32', type: 'image/png' } },
        { tag: 'link', attrs: { rel: 'icon', href: '/favicon-16.png', sizes: '16x16', type: 'image/png' } },
        { tag: 'link', attrs: { rel: 'apple-touch-icon', href: '/apple-touch-icon.png' } },
        { tag: 'link', attrs: { rel: 'manifest', href: '/site.webmanifest' } },
        { tag: 'meta', attrs: { name: 'theme-color', content: '#f4f5f7', media: '(prefers-color-scheme: light)' } },
        { tag: 'meta', attrs: { name: 'theme-color', content: '#0d0e11', media: '(prefers-color-scheme: dark)' } },
        // Дизайн ставится до первой отрисовки: по умолчанию минималистичный.
        {
          tag: 'script',
          content: `(function(){var d='minimal';try{if(localStorage.getItem('docs-design')==='classic')d='classic';}catch(e){}document.documentElement.dataset.design=d;})();`,
        },
        // Группы меню всегда раскрыты: подпись группы не сворачивает её ни мышью,
        // ни с клавиатуры, сохранённое «свёрнуто» не восстанавливается.
        {
          tag: 'script',
          content: `document.addEventListener('DOMContentLoaded',function(){document.querySelectorAll('.sidebar-content details').forEach(function(d){d.open=true;var s=d.querySelector('summary');if(!s)return;s.tabIndex=-1;s.addEventListener('click',function(e){e.preventDefault();});});});`,
        },
        // Значки пунктов меню (src/icons.mjs).
        { tag: 'style', content: iconCss },
        // Старый одностраничник жил на якорях (#install, #faq и т.д.) — такие
        // ссылки ходят по чатам и постам. Переводим их на новые страницы.
        {
          tag: 'script',
          content: `(function(){if(location.pathname!=='/'&&location.pathname!=='/index.html')return;var m={'#features':'/start/features/','#channels':'/ops/channels/','#install':'/start/install/','#wizard':'/start/wizard/','#admin':'/admin/overview/','#payments':'/start/features/','#webhooks':'/payments/webhooks/','#commands':'/reference/commands/','#env':'/reference/env/','#update':'/ops/update/','#faq':'/ops/faq/'};var t=m[location.hash];if(t)location.replace(t);})();`,
        },

        // Ссылки со старыми кириллическими якорями ведут на тот же заголовок.
        { tag: 'script', content: legacyAnchorScript },

        { tag: 'meta', attrs: { property: 'og:image', content: 'https://remna.shop/og.png' } },
        { tag: 'meta', attrs: { name: 'twitter:card', content: 'summary_large_image' } },
      ],
      sidebar: [
        {
          label: 'Начало',
          items: [
            { label: 'Введение', slug: 'start/features', attrs: icon('book') },
            { label: 'Что подготовить', slug: 'start/requirements', attrs: icon('checklist') },
            { label: 'Требования к серверу', slug: 'start/server', attrs: icon('cpu') },
          ],
        },
        {
          label: 'Установка',
          items: [
            { label: 'Установка за 4 шага', slug: 'start/install', attrs: icon('download') },
            { label: 'Мастер настройки', slug: 'start/wizard', attrs: icon('wand') },
            { label: 'После установки', slug: 'start/after-install', attrs: icon('check') },
          ],
        },
        {
          label: 'Возможности',
          items: [
            { label: 'Админка: обзор', slug: 'admin/overview', attrs: icon('grid') },
            { label: 'Продажи', slug: 'admin/sales', attrs: icon('cart') },
            { label: 'Маркетинг', slug: 'admin/marketing', attrs: icon('megaphone') },
            { label: 'Интерфейс', slug: 'admin/interface', attrs: icon('layout') },
            { label: 'Тексты бота', slug: 'admin/texts', attrs: icon('text') },
            { label: 'Пользователи', slug: 'admin/users', attrs: icon('users') },
            { label: 'Система', slug: 'admin/system', attrs: icon('cog') },
            { label: 'Как это выглядит', slug: 'screens', attrs: icon('image') },
          ],
        },
        {
          label: 'Оплата',
          items: [
            { label: 'Вебхуки и свой домен', slug: 'payments/webhooks', attrs: icon('globe') },
            { label: 'Tribute', slug: 'payments/tribute', attrs: icon('star') },
            { label: 'ЮMoney', slug: 'payments/yoomoney', attrs: icon('wallet') },
          ],
        },
        {
          label: 'Обслуживание',
          items: [
            { label: 'Обновление и бэкап', slug: 'ops/update', attrs: icon('refresh') },
            { label: 'Каналы релизов', slug: 'ops/channels', attrs: icon('branch') },
            { label: 'Если что-то не работает', slug: 'ops/troubleshooting', attrs: icon('lifebuoy') },
            { label: 'Вопросы и ответы', slug: 'ops/faq', attrs: icon('help') },
          ],
        },
        {
          label: 'Дополнительно',
          items: [
            { label: 'Команды бота', slug: 'reference/commands', attrs: icon('terminal') },
            { label: 'Переменные окружения', slug: 'reference/env', attrs: icon('sliders') },
            { label: 'Права токена панели', slug: 'reference/panel-scopes', attrs: icon('key') },
            { label: 'Лицензия', slug: 'license', attrs: icon('file') },
          ],
        },
      ],
    }),
  ],
});
