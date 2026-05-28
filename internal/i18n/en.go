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
	"status.line":    "📊 <b>Service status</b>\n\nPanel: reachable ✅\nUsers in panel: %d\nDatabase: %s\nPanel mode: %s\nEnabled payments: %s",
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
	"menu.welcome":           "👋 <b>Welcome, %s!</b>\n\nThis bot gives you VPN access: pick a plan, pay and get a connection key in a couple of minutes.\n\nChoose an action below.",
	"btn.buy":                "💳 Buy",
	"btn.status":             "📊 Status",
	"btn.p2p":                "⚙️ P2P",
	"btn.emoji":              "🎨 Emoji",
	"btn.banner":             "🖼 Banner",
	"btn.update":             "🔄 Update",
	"btn.register":           "✅ Register",
	"btn.home":               "🏠 Home",
	"btn.cancel":             "⬅️ Cancel",
	"btn.reconfig":           "🔧 Reconfigure (DB & panel)",
	"menu.cat_iface":         "🎨 Interface",
	"menu.cat_pay":           "💳 Payment settings",
	"menu.cat_manage":        "🛠 Bot management",
	"menu.admin_title":       "🛠 <b>Admin panel</b>\nChoose a section:",
	"register.prompt":        "👋 Hi, %s!\nTo continue, please complete a quick registration.",
	"update.done":            "✅ Bot updated and restarted.",
	"welcome.title":          "🖼 <b>Start banner</b>\nImage and text the user sees on /start.",
	"welcome.btn_image":      "🖼 Image",
	"welcome.btn_text":       "📝 Text",
	"welcome.ask_image":      "Send a photo (upload) or an image URL. It will become the start banner.",
	"welcome.ask_text":       "Send a message with exactly the formatting and line breaks you want — I'll keep it 1:1.",
	"welcome.saved":          "✅ Saved.",
	"emoji.title":            "🎨 <b>Bot emoji</b>\n\nReplace the bot's standard emoji with your animated (premium) ones. Works because the bot owner has Telegram Premium.\n\nTap an emoji and send its animated version. A check mark means it's set.\n\nWhere used:",
	"emoji.btn_done":         "Done",
	"emoji.ask_one":          "Send the animated version for %s as a single emoji.",
	"emoji.set_ok":           "✅ Set for %s.",
	"emoji.none_in_msg":      "No animated (premium) emoji in the message. Send a premium emoji.",

	"menu.iface_title": "🎨 <b>Interface</b>\n\n" +
		"Configure how the bot looks to users: the start banner (image and welcome text) " +
		"and animated premium emoji in messages.\n\n" +
		"Choose what to set up:",
	"menu.pay_title": "💳 <b>Payment settings</b>\n\n" +
		"Ways to accept payment. Currently available: card transfer (P2P) with manual review — " +
		"card details, rotation between them, prices per period and the squad for new accounts.\n\n" +
		"Choose a section:",
	"menu.manage_title": "🛠 <b>Bot management</b>\n\n" +
		"Utility functions: panel connection status, bot users (block and delete affect the bot " +
		"only), bot updates and reconfiguring the DB and panel connection.\n\n" +
		"Choose an action:",

	"btn.users":       "👥 Users",
	"btn.back":        "⬅️ Back",
	"btn.block":       "🚫 Block",
	"btn.unblock":     "✅ Unblock",
	"btn.delete":      "🗑 Delete from bot",
	"btn.del_confirm": "🗑 Confirm delete",
	"btn.prev":        "⬅️",
	"btn.next":        "➡️",

	"users.title": "👥 <b>Bot users</b>\nTotal: %d · page %d/%d\n\n" +
		"People registered in the bot. Block and delete affect the bot ONLY and do NOT touch " +
		"panel accounts.\n\nChoose a user:",
	"users.empty": "👥 No users yet.",
	"user.card": "👤 <b>%s</b>\nRegistered: %s\nP2P access: %s\nStatus: %s\n\n" +
		"⚠️ The actions below change bot access only; the panel account stays untouched.",
	"user.active":           "active ✅",
	"user.blocked":          "blocked 🚫",
	"user.yes":              "yes ✅",
	"user.no":               "no",
	"user.blocked_done":     "🚫 User blocked in the bot.",
	"user.unblocked_done":   "✅ User unblocked.",
	"user.deleted":          "🗑 User deleted from the bot (panel account untouched).",
	"user.you_blocked":      "🚫 Your access to the bot is restricted. Contact the administrator.",
	"user.card_confirm_del": "🗑 Delete user <code>%s</code> from the bot? The panel account is NOT affected.",
	"btn.p2p_allow":         "✅ Allow P2P",
	"btn.p2p_deny":          "🚫 Revoke P2P",
	"btn.mysubs":            "📲 My subscriptions",
	"subs.none":             "📲 You don't have an active subscription yet.\nPress «Buy» to get access.",
	"subs.show":             "📲 <b>Your subscription</b>\n\nConnection link:\n<code>%s</code>",
	"btn.stars":             "⭐ Telegram Stars",
	"btn.payments":          "📒 Payments log",
	"method.stars_btn":      "⭐ Pay with Stars (%d ⭐)",
	"stars.no_price":        "No Stars price set for this period.",
	"stars.invoice_title":   "Subscription for %d mo",
	"stars.invoice_desc":    "VPN access for %d mo. Paid with Telegram Stars.",
	"stars.paid_ok":         "✅ Stars payment received, subscription activated!\n%s",
	"stars.fail":            "❌ Payment went through but activation failed: %s\nPlease contact the admin.",
	"admin.stars_title":     "⭐ <b>Telegram Stars</b>\nStatus: %s\nPrices: %s",
	"admin.stars_ask_price": "Enter the Stars price for %d mo (integer):",
	"payments.title":        "📒 <b>Payments log</b>\nTotal: %d · page %d/%d\n\ndate · method · user · period · amount · status:",
	"payments.empty":        "📒 No payments yet.",
	"payments.line":         "%s · %s · <code>%d</code> · %dmo · %s · %s",
	"payments.st_paid":      "paid ✅",
	"payments.st_rejected":  "rejected ❌",
	"btn.yookassa":          "💳 YooKassa",
	"btn.pricing":           "💲 Base prices",
	"method.yk_btn":         "💳 Card (YooKassa) — %s",
	"yk.no_price":           "No card price set for this period.",
	"yk.not_configured":     "Card payment is not configured by the admin yet.",
	"yk.invoice_desc":       "VPN subscription for %d mo.",
	"yk.fail":               "❌ YooKassa payment error: %s",
	"yk.pay_prompt":         "💳 Payment for %d mo, amount %s.\n\nPress «Pay», complete payment on the YooKassa page, then come back and press «Check payment».",
	"yk.btn_pay":            "💳 Pay",
	"yk.btn_check":          "🔄 Check payment",
	"yk.pending":            "🕒 Payment not received yet. If you paid — wait a minute and press «Check payment» again.",
	"yk.paid_ok":            "✅ Payment received, subscription activated!\n%s",
	"admin.yk_title":        "💳 <b>YooKassa</b>\nStatus: %s\nShop ID: %s\nSecret key: %s\nReturn URL: %s\nPrices: %s",
	"admin.yk_btn_shop":     "Shop ID",
	"admin.yk_btn_secret":   "Secret key",
	"admin.yk_btn_return":   "Return URL",
	"admin.yk_ask_shop":     "Enter the Shop ID from your YooKassa dashboard:",
	"admin.yk_ask_secret":   "Enter the YooKassa secret key (stored encrypted):",
	"admin.yk_ask_return":   "Enter the Return URL (where to return after payment), e.g. https://t.me/your_bot:",
	"admin.yk_ask_price":    "Enter the card price for %d mo (e.g. 150.00):",
	"pricing.title":         "💲 <b>Base prices</b>\nPrices: %s\nCurrency: %s\n\nShared price list for money methods. A specific method can override it in its own settings.",
	"pricing.btn_base":      "Base price",
	"pricing.btn_cur":       "Currency",
	"admin.ask_base_price":  "Enter the base price for %d mo (e.g. 150):",
	"admin.ask_currency":    "Enter the currency symbol for money methods (e.g. RUB, ₽, USD):",

	"squad.title": "🎯 <b>Squad for new users</b>\nCurrent: %s\n\n" +
		"Pick a squad from the panel — accounts created by the bot will be placed into it:",
	"squad.none":              "not set",
	"squad.clear":             "🚫 No squad",
	"squad.manual":            "✏️ Enter UUID manually",
	"squad.refresh":           "🔄 Refresh list",
	"squad.empty":             "No squads found in the panel. Enter a UUID manually or create a squad in the panel.",
	"squad.fail":              "❌ Could not fetch squads from the panel: %s\nYou can enter a UUID manually with the button below.",
	"squad.set_ok":            "✅ Squad saved.",
	"btn.section_banners":     "🖼 Section banners",
	"banners.title":           "🖼 <b>Section banners</b>\n\nPick a section to see its current image and replace it with your own. Each bot screen has its own banner; «Reset» restores the default image from the repo.",
	"banners.section_caption": "<b>%s</b>\n\nThis is the current banner for this section (what users see). Tap «Upload new» and send a photo to change it. «Reset» restores the default.",
	"banners.ask_upload":      "📤 Send a photo to be used as the banner for «%s».\n\nLandscape works best (e.g. 1280×640). Telegram will compress automatically.",
	"banners.btn_upload":      "📤 Upload new",
	"banners.btn_reset":       "↩️ Reset to default",
	"btn.subdomain":           "🌐 Subscription domain",
	"subdomain.title":         "🌐 <b>Subscription domain override</b>\n\nStatus: %s\nCurrent domain: <code>%s</code>\n\nWhen enabled, the bot rewrites the host in the subscription link, keeping the path and shortId. Useful if you want to expose subscriptions via a single brand domain, hiding the panel host.",
	"subdomain.on":            "on ✅",
	"subdomain.off":           "off ❌",
	"subdomain.btn_change":    "✏️ Change domain",
	"subdomain.btn_clear":     "↩️ Clear (use panel host)",
	"subdomain.ask":           "✏️ Send the subscription domain (e.g. <code>vpn.mybrand.io</code>), or «-» to disable rewriting.",
	"btn.close":               "❌ Close",
	"btn.apilog":              "📡 API log",
	"apilog.title":            "📡 <b>Outgoing API calls log (panel)</b>\nTotal: %d · page %d/%d",
	"apilog.empty":            "📡 API log is empty — bot has not made any panel calls yet (or buffer was cleared).",
	"apilog.no_panel":         "📡 Panel not connected — nothing to log.",
	"apilog.btn_refresh":      "🔄 Refresh",
	"apilog.btn_clear":        "🧹 Clear",
}
