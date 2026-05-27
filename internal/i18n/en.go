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
		"• Reverse proxies (Caddy, nginx, etc.): https://docs.rw/docs/install/reverse-proxies/\n" +
		"• Panel protection: https://docs.rw/docs/security/caddy-with-minimal-setup/\n" +
		"No cookie in our sense; machines use X-API-Key (asked only if enabled).\n\n" +
		"🛠 <b>eGames script (nginx, cookie auth)</b>\n" +
		"Sources:\n" +
		"• Repository: https://github.com/eGamesAPI/remnawave-reverse-proxy\n" +
		"• Wiki: https://wiki.egam.es/\n" +
		"Sets up nginx with cookie protection — you'll need to provide it.",
	"step.install.choose_docs":   "📘 Official (docs)",
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

	"update.starting":        "⏳ Starting update (pull + restart). The bot will restart for a couple of seconds…",
	"update.fail":            "❌ Could not start the update: %s",
	"update.not_available":   "Self-update is unavailable: the bot runs without docker.sock access.",
	"menu.installed":         "Bot is ready 👋\nChoose an action:",
	"menu.buy":               "💳 Buy subscription",
	"menu.admin_hint":        "⚙️ Admin: /p2p — P2P payments · /emoji — animated emoji.",
	"buy.choose_plan":        "📦 Choose a subscription period:",
	"buy.no_plans":           "No plans configured yet, check back later.",
	"buy.plan_btn":           "%d mo — %s",
	"buy.choose_method":      "💳 Choose a payment method:",
	"buy.no_methods":         "No payment methods configured yet.",
	"method.p2p_btn":         "💳 Card transfer (P2P)",
	"p2p.need_approval":      "🔒 For card-transfer payment the admin must approve this method for you. Request sent — you'll be notified once approved.",
	"p2p.user_approved":      "✅ Card-transfer payment approved for you. Press «Buy» and choose this method again.",
	"p2p.card":               "💳 Payment for %d mo, amount %s\n\nTransfer to:\n<code>%s</code>\n\nAfter paying press «I paid» and send a screenshot. Manual review by admin.",
	"p2p.paid_btn":           "✅ I paid",
	"p2p.send_screenshot":    "📸 Send the payment screenshot as a single photo.",
	"p2p.submitted":          "🕒 Screenshot received and sent for review. Please wait for confirmation.",
	"p2p.no_cards":           "Payment details are not configured, contact the admin.",
	"p2p.user_paid_ok":       "✅ Payment confirmed, subscription activated!\n%s",
	"p2p.user_paid_rejected": "❌ Payment rejected.\nReason: %s",
	"admin.p2p_title":        "⚙️ <b>Card-transfer payment (P2P)</b>\nStatus: %s\nCards: %d · Rotation: %s\nPrices: %s\nSquad: %s",
	"admin.on":               "on ✅",
	"admin.off":              "off ❌",
	"admin.yes":              "yes",
	"admin.no":               "no",
	"admin.none":             "—",
	"admin.btn_toggle":       "On/Off",
	"admin.btn_cards":        "Cards",
	"admin.btn_prices":       "Prices",
	"admin.btn_squad":        "Squad",
	"admin.btn_rotate":       "Rotation",
	"admin.ask_cards":        "Send card details in one message; separate multiple cards with `;`.",
	"admin.ask_price_month":  "Which period to set the price for?",
	"admin.ask_price":        "Enter the price for %d mo (e.g. 150):",
	"admin.ask_squad":        "Send the squad UUID for new users (or `-` to clear):",
	"admin.saved":            "✅ Saved.",
	"admin.user_request":     "🔔 User <code>%d</code> requests access to card-transfer payment. Approve?",
	"admin.btn_user_ok":      "✅ Approve access",
	"admin.btn_user_no":      "❌ Deny",
	"admin.user_denied":      "P2P access denied for the user.",
	"admin.user_ok_done":     "✅ Access granted, user notified.",
	"admin.payment_caption":  "💸 User <code>%d</code> paid %d mo, amount %s.\nRequest #%d — check the screenshot.",
	"admin.btn_pay_ok":       "✅ Confirm",
	"admin.btn_pay_no":       "❌ Reject",
	"admin.ask_reason":       "Enter the rejection reason (will be sent to the user):",
	"admin.done":             "✅ Done.",
	"admin.provision_fail":   "❌ Panel creation error: %s\nRequest not confirmed.",
	"admin.not_found":        "Request not found or already processed.",
	"admin.ask_emoji":        "Send the animated (premium) emoji you want in a single message — I will use them in all bot messages.",
	"admin.emoji_saved":      "✅ Saved animated emoji: %d. They will appear in bot messages.",
}
