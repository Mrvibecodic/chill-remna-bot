package i18n

var en = map[string]string{
	"setup.welcome": "👋 Hi! This is the Remnawave bot setup wizard.\n\n" +
		"I'll ask a few questions with explanations — after each answer we move to the next step.\n\n" +
		"<b>Step 1.</b> Which language should we use?",
	"setup.not_admin": "The bot is not configured yet. Only the administrator can run the setup wizard.",

	"step.db.title": "🗄 <b>Step 2. Database</b>\n\n" +
		"Where should the bot store its data? You can switch later — migration is built in.",
	"step.db.body": "📄 <b>SQLite (file)</b> — recommended for starting out and small projects.\n" +
		"• Nothing to install separately, the whole DB is one file.\n" +
		"• Backup = copy the file. RAM usage — a few MB.\n" +
		"• Downside: single writer; under heavy purchase load it may lag.\n" +
		"• Rule of thumb: comfortable up to a few thousand users.\n\n" +
		"🐘 <b>PostgreSQL 17</b> — for large, high-load projects.\n" +
		"• Many concurrent writes, fast analytics, reliable at scale.\n" +
		"• Downside: a separate container, ~50–150 MB RAM, backup via pg_dump.\n" +
		"• Rule of thumb: tens of thousands of users and active sales.",
	"step.db.choose_sqlite":   "📄 SQLite",
	"step.db.choose_postgres": "🐘 PostgreSQL 17",
	"step.pgdsn.ask": "Enter the PostgreSQL connection string (DATABASE_URL), e.g.:\n" +
		"<code>postgres://user:pass@host:5432/dbname?sslmode=disable</code>",
	"step.db.pg_starting": "⏳ Bringing up PostgreSQL and switching to it (no bot restart)…",
	"step.db.pg_ok":       "✅ PostgreSQL is up, the bot switched to it.",
	"step.db.pg_failed":   "⚠️ Could not bring up PostgreSQL: %s\nStaying on SQLite — it works fine; you can switch to Postgres later.",

	"step.location.title": "📍 <b>Step 3. Where does the bot run relative to the panel?</b>\n\n" +
		"🏠 <b>Local</b> (same docker network as the panel) — recommended: safer and simpler, " +
		"we reach the panel directly, no cookie/key protection needed.\n\n" +
		"🌐 <b>Remote</b> — the bot is on another server, we go through the panel's public HTTPS domain.",
	"step.location.choose_local":  "🏠 Local",
	"step.location.choose_remote": "🌐 Remote",

	"step.install.title": "🧩 <b>Step 4. How was the panel installed?</b>\n\n" +
		"This tells us the type of public-access protection.\n\n" +
		"📘 <b>Official (Caddy)</b>\n" +
		"Sources:\n" +
		"• Quick start: https://docs.rw/docs/overview/quick-start/\n" +
		"• Caddy reverse proxy: https://docs.rw/docs/install/reverse-proxies/caddy/\n" +
		"• Panel protection: https://docs.rw/docs/security/caddy-with-minimal-setup/\n" +
		"No cookie in our sense; machines use X-API-Key (asked only if enabled).\n\n" +
		"🛠 <b>eGames script (nginx)</b>\n" +
		"Sources:\n" +
		"• Repository: https://github.com/eGamesAPI/remnawave-reverse-proxy\n" +
		"• Wiki: https://wiki.egam.es/\n" +
		"Sets up nginx with cookie protection — you'll need to provide it.",
	"step.install.choose_docs":   "📘 Official (Caddy)",
	"step.install.choose_egames": "🛠 eGames script (nginx)",

	"step.url.ask": "🔗 Enter the panel URL (e.g. <code>https://panel.example.com</code>).",

	"step.token.ask": "🔑 Enter the panel API token.\n\n" +
		"Where to get it: Remnawave dashboard → create an API token (role API). " +
		"A regular admin login/password won't work for the API.",

	"step.cookie.ask": "🍪 Your panel is behind nginx with cookie protection.\n\n" +
		"On the panel server open <code>/opt/remnawave/nginx.conf</code> and find this block:\n" +
		"<pre>map $http_cookie $auth_cookie {\n    default 0;\n    \"~*NAME=VALUE\" 1;\n}</pre>\n" +
		"NAME and VALUE are two random 8-letter strings (e.g. <code>XkPmtZQr=fNbWqLpA</code>). " +
		"The same pair is in the installer URL: <code>https://YOUR-DOMAIN/auth/login?NAME=VALUE</code>.\n\n" +
		"Send it here as <code>NAME=VALUE</code>.",

	"step.apikey.ask_protected": "🔐 Is the <code>/api</code> path protected in your Caddy (Caddyfile with-protected-api-route)?",
	"step.apikey.yes":           "Yes, protected",
	"step.apikey.no":            "No, open",
	"step.apikey.ask":           "🔐 Enter the caddy-security X-API-Key (apikeys section in the portal).",

	"step.verify.checking": "⏳ Checking the panel connection…",
	"step.verify.ok":       "✅ Panel is reachable! Users in panel: %d.\n\nSetup complete. Commands: /status, /update",
	"step.verify.fail":     "❌ Could not connect: %s\n\nFix the details and try again: /setup",

	"installed.hint": "The bot is already set up. Available: /status, /update. Reinstall: /setup.",
	"status.line":    "📊 <b>Status</b>\nPanel: reachable ✅\nUsers: %d\nDB: %s · Mode: %s",
	"status.fail":    "📊 Panel unavailable: %s",
	"setup.restart":  "Restarting the setup wizard.",

	"update.starting":      "⏳ Starting update (pull + restart). The bot will restart for a couple of seconds…",
	"update.fail":          "❌ Could not start the update: %s",
	"update.not_available": "Self-update is unavailable: the bot runs without docker.sock access.",
}
