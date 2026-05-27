package i18n

// Строки в HTML-разметке Telegram (ParseMode=HTML).
var ru = map[string]string{
	"setup.welcome": "👋 Привет! Это мастер первичной установки бота Remnawave.\n\n" +
		"Я задам несколько вопросов с пояснениями — после каждого ответа перейдём к следующему шагу.\n\n" +
		"<b>Шаг 1.</b> С какого языка начнём?",
	"setup.not_admin": "Бот ещё не настроен. Доступ к мастеру установки есть только у администратора.",

	"step.db.title": "🗄 <b>Шаг 2. Выбор базы данных</b>\n\n" +
		"Где хранить данные бота? Переключиться можно позже — миграция встроена.",
	"step.db.body": "📄 <b>SQLite (файл)</b> — рекомендуем для старта и небольших проектов.\n" +
		"• Ничего не нужно ставить отдельно, вся база — один файл.\n" +
		"• Бэкап = скопировать файл. Память — единицы МБ.\n" +
		"• Минус: один писатель за раз; при большом потоке покупок возможны подтормаживания.\n" +
		"• Ориентир: комфортно примерно до нескольких тысяч пользователей.\n\n" +
		"🐘 <b>PostgreSQL 17</b> — для больших и нагруженных проектов.\n" +
		"• Много одновременных записей, быстрая аналитика, надёжность на масштабе.\n" +
		"• Минус: отдельный контейнер, ~50–150 МБ RAM, бэкап через pg_dump.\n" +
		"• Ориентир: десятки тысяч пользователей и активные продажи.",
	"step.db.choose_sqlite":   "📄 SQLite",
	"step.db.choose_postgres": "🐘 PostgreSQL 17",
	"step.pgdsn.ask": "Введите строку подключения к PostgreSQL (DATABASE_URL), например:\n" +
		"<code>postgres://user:pass@host:5432/dbname?sslmode=disable</code>",
	"step.db.pg_starting": "⏳ Поднимаю PostgreSQL и переключаюсь на него (бот не перезапускается)…",
	"step.db.pg_ok":       "✅ PostgreSQL поднят, бот переключён на него.",
	"step.db.pg_failed":   "⚠️ Не удалось поднять PostgreSQL: %s\nОстаюсь на SQLite — это рабочий вариант, перейти на Postgres можно позже.",

	"step.location.title": "📍 <b>Шаг 3. Где расположен бот относительно панели?</b>\n\n" +
		"🏠 <b>Локально</b> (в одной docker-сети с панелью) — рекомендуем: безопаснее и проще, " +
		"обращаемся к панели напрямую, защита по куке/ключу не нужна.\n\n" +
		"🌐 <b>Удалённо</b> — бот на другом сервере, ходим через публичный HTTPS-домен панели.",
	"step.location.choose_local":  "🏠 Локально",
	"step.location.choose_remote": "🌐 Удалённо",

	"step.install.title": "🧩 <b>Шаг 4. Как была установлена панель?</b>\n\n" +
		"Это нужно, чтобы понять тип защиты публичного доступа.\n\n" +
		"📘 <b>Официально (Caddy)</b>\n" +
		"Источники:\n" +
		"• Быстрый старт: https://docs.rw/docs/overview/quick-start/\n" +
		"• Reverse-proxy Caddy: https://docs.rw/docs/install/reverse-proxies/caddy/\n" +
		"• Защита панели: https://docs.rw/docs/security/caddy-with-minimal-setup/\n" +
		"Куки в нашем смысле нет; для машины используется X-API-Key (спрошу, только если включён).\n\n" +
		"🛠 <b>Скрипт eGames (nginx)</b>\n" +
		"Источники:\n" +
		"• Репозиторий: https://github.com/eGamesAPI/remnawave-reverse-proxy\n" +
		"• Вики: https://wiki.egam.es/\n" +
		"Ставит nginx с защитой по куке — её нужно будет указать.",
	"step.install.choose_docs":   "📘 Официально (Caddy)",
	"step.install.choose_egames": "🛠 Скрипт eGames (nginx)",

	"step.url.ask": "🔗 Введите URL панели (например, <code>https://panel.example.com</code>).",

	"step.token.ask": "🔑 Введите API-token панели.\n\n" +
		"Где взять: дашборд Remnawave → создать API-token (роль API). " +
		"Обычный логин/пароль администратора для API не подходит.",

	"step.cookie.ask": "🍪 Ваша панель за nginx с защитой по куке.\n\n" +
		"Откройте на сервере панели файл <code>/opt/remnawave/nginx.conf</code> и найдите блок:\n" +
		"<pre>map $http_cookie $auth_cookie {\n    default 0;\n    \"~*ИМЯ=ЗНАЧЕНИЕ\" 1;\n}</pre>\n" +
		"ИМЯ и ЗНАЧЕНИЕ — два случайных набора из 8 латинских букв (напр. <code>XkPmtZQr=fNbWqLpA</code>). " +
		"Та же пара есть в ссылке установщика: <code>https://ВАШ-ДОМЕН/auth/login?ИМЯ=ЗНАЧЕНИЕ</code>.\n\n" +
		"Пришлите её сюда в формате <code>ИМЯ=ЗНАЧЕНИЕ</code>.",

	"step.apikey.ask_protected": "🔐 Защищён ли путь <code>/api</code> в вашем Caddy (Caddyfile with-protected-api-route)?",
	"step.apikey.yes":           "Да, защищён",
	"step.apikey.no":            "Нет, открыт",
	"step.apikey.ask":           "🔐 Введите X-API-Key от caddy-security (раздел apikeys в портале).",

	"step.verify.checking": "⏳ Проверяю связь с панелью…",
	"step.verify.ok":       "✅ Панель на связи! Пользователей в панели: %d.\n\nУстановка завершена. Команды: /status, /update",
	"step.verify.fail":     "❌ Не удалось подключиться: %s\n\nИсправьте данные и попробуйте снова: /setup",

	"installed.hint": "Бот уже установлен. Доступно: /status, /update. Переустановка: /setup.",
	"status.line":    "📊 <b>Статус</b>\nПанель: на связи ✅\nПользователей: %d\nБД: %s · Режим: %s",
	"status.fail":    "📊 Панель недоступна: %s",
	"setup.restart":  "Запускаю мастер установки заново.",

	"update.starting":      "⏳ Запускаю обновление (pull + перезапуск). Бот на пару секунд перезапустится…",
	"update.fail":          "❌ Не удалось запустить обновление: %s",
	"update.not_available": "Самообновление недоступно: бот запущен без доступа к docker.sock.",
}
