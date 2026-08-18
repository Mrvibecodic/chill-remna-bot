// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

// Сайт документации chill-remna-bot. Собирается в статику и выкладывается на
// GitHub Pages (домен remna.shop), поэтому никакого рантайма на сервере нет.
export default defineConfig({
  site: 'https://remna.shop',
  trailingSlash: 'ignore',
  build: { format: 'directory' },
  integrations: [
    starlight({
      title: 'Chill Remna bot',
      description:
        'Телеграм-бот «магазин + админка» для панели Remnawave: тарифы, оплаты, триал, рефералка, мини-апп и веб-кабинет.',
      defaultLocale: 'root',
      locales: { root: { label: 'Русский', lang: 'ru' } },
      customCss: ['./src/styles/theme.css'],
      social: [
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
        { tag: 'meta', attrs: { name: 'theme-color', content: '#0a0d15' } },
        // Старый одностраничник жил на якорях (#install, #faq и т.д.) — такие
        // ссылки ходят по чатам и постам. Переводим их на новые страницы.
        {
          tag: 'script',
          content: `(function(){if(location.pathname!=='/'&&location.pathname!=='/index.html')return;var m={'#features':'/start/features/','#channels':'/ops/channels/','#install':'/start/install/','#wizard':'/start/wizard/','#admin':'/admin/overview/','#payments':'/start/features/','#webhooks':'/payments/webhooks/','#commands':'/reference/commands/','#env':'/reference/env/','#update':'/ops/update/','#faq':'/ops/faq/'};var t=m[location.hash];if(t)location.replace(t);})();`,
        },

        { tag: 'meta', attrs: { property: 'og:image', content: 'https://remna.shop/icon-512.png' } },
      ],
      sidebar: [
        {
          label: 'Начало',
          items: [
            { label: 'Что умеет бот', slug: 'start/features' },
            { label: 'Что нужно перед стартом', slug: 'start/requirements' },
            { label: 'Установка за 4 шага', slug: 'start/install' },
            { label: 'Мастер настройки', slug: 'start/wizard' },
          ],
        },
        {
          label: 'Админка',
          items: [
            { label: 'Обзор', slug: 'admin/overview' },
            { label: 'Продажи', slug: 'admin/sales' },
            { label: 'Маркетинг', slug: 'admin/marketing' },
            { label: 'Интерфейс', slug: 'admin/interface' },
            { label: 'Пользователи', slug: 'admin/users' },
            { label: 'Система', slug: 'admin/system' },
          ],
        },
        {
          label: 'Платежи',
          items: [{ label: 'Вебхуки и reverse-proxy', slug: 'payments/webhooks' }],
        },
        {
          label: 'Справочник',
          items: [
            { label: 'Команды бота', slug: 'reference/commands' },
            { label: 'Переменные окружения', slug: 'reference/env' },
          ],
        },
        {
          label: 'Эксплуатация',
          items: [
            { label: 'Обновление и бэкап', slug: 'ops/update' },
            { label: 'Каналы релизов', slug: 'ops/channels' },
            { label: 'Если что-то не работает', slug: 'ops/troubleshooting' },
            { label: 'FAQ', slug: 'ops/faq' },
          ],
        },
        {
          label: 'Ещё',
          items: [
            { label: 'Скриншоты', slug: 'screens' },
            { label: 'Лицензия', slug: 'license' },
          ],
        },
      ],
    }),
  ],
});
