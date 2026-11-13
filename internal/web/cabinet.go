package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// cabinetJWTTTL is the lifetime of a web-cabinet session token. Longer than the
// Mini App's (the cabinet is a normal site, not re-opened from a chat).
const cabinetJWTTTL = 7 * 24 * time.Hour

// loginTTL bounds how old Telegram Login Widget data may be (anti-replay).
// loginTTL — сколько живёт подпись виджета входа. Виджет перебрасывает в
// кабинет сразу после нажатия, так что окно нужно короткое: сутки означали, что
// утёкшая подпись сутки меняется на свежие пропуска.
const loginTTL = 15 * time.Minute

// validateTelegramLogin verifies Telegram Login Widget data per the official
// algorithm: secret = SHA256(botToken); the data-check-string is every provided
// field except "hash", sorted by key as key=value joined by '\n'; the hex HMAC
// of that with secret must equal "hash". Returns the authenticated Telegram id.
func validateTelegramLogin(fields map[string]string, botToken string, ttl time.Duration) (int64, error) {
	hash := fields["hash"]
	if hash == "" || botToken == "" {
		return 0, errAuth
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		if k == "hash" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(fields[k])
	}
	secret := sha256.Sum256([]byte(botToken))
	want := hex.EncodeToString(hmacSHA256(secret[:], []byte(b.String())))
	if !hmac.Equal([]byte(want), []byte(hash)) {
		return 0, errAuth
	}
	if ttl > 0 {
		ad, err := strconv.ParseInt(fields["auth_date"], 10, 64)
		if err != nil || ad <= 0 {
			return 0, errAuth
		}
		// Как и у мини-аппа: дата из будущего раньше принималась всегда.
		if d := time.Since(time.Unix(ad, 0)); d > ttl || d < -authClockSkew {
			return 0, errAuth
		}
	}
	id, err := strconv.ParseInt(fields["id"], 10, 64)
	if err != nil || id == 0 {
		return 0, errAuth
	}
	return id, nil
}

// tgLoginReq — данные входа через Telegram: id_token новой библиотеки входа
// либо поля прежнего виджета с подписью hash (их ещё может прислать открытая
// до обновления вкладка).
type tgLoginReq struct {
	IDToken   string `json:"id_token"`
	Verifier  string `json:"verifier"`
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
	PhotoURL  string `json:"photo_url"`
	AuthDate  int64  `json:"auth_date"`
	Hash      string `json:"hash"`
}

func (s *Server) cabinetTGUser(req tgLoginReq) (tgLoginUser, error) {
	if req.IDToken != "" {
		return s.checkTGIDToken(req.IDToken, req.Verifier)
	}
	fields := map[string]string{
		"id":        strconv.FormatInt(req.ID, 10),
		"auth_date": strconv.FormatInt(req.AuthDate, 10),
		"hash":      req.Hash,
	}
	for k, v := range map[string]string{"first_name": req.FirstName, "last_name": req.LastName, "username": req.Username, "photo_url": req.PhotoURL} {
		if v != "" {
			fields[k] = v
		}
	}
	id, err := validateTelegramLogin(fields, s.mini.MiniBotToken(), loginTTL)
	if err != nil {
		return tgLoginUser{}, err
	}
	return tgLoginUser{ID: id, Username: req.Username, FirstName: req.FirstName}, nil
}

// handleFlag serves a self-hosted country-flag SVG (see privacy F1: flags are
// fetched once at startup and served from this server, never from a third-party
// CDN at the visitor's browser).
func (s *Server) handleFlag(w http.ResponseWriter, r *http.Request) {
	if s.mini == nil {
		http.NotFound(w, r)
		return
	}
	code := strings.TrimSuffix(r.PathValue("code"), ".svg")
	b, ok := s.mini.CabinetFlag(code)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
	_, _ = w.Write(b)
}

// handleRobots blocks search engines/crawlers across the whole bot domain
// (webhooks, mini-app, cabinet) — none of it should be indexable.
func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
}

func (s *Server) cabinetOK() bool { return s.mini != nil && s.mini.CabinetEnabled() }

func (s *Server) issueCabinetToken(ctx context.Context, w http.ResponseWriter, tgID int64) {
	tok := issueJWT(tgID, true, jwtKey(s.mini.MiniBotToken()), cabinetJWTTTL, s.mini.SessionVersion(), s.mini.SessionEpoch(ctx, tgID))
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "expires_in": int(cabinetJWTTTL.Seconds())})
}

// handleCabinetConfig returns the public info the cabinet login page needs.
func (s *Server) handleCabinetConfig(w http.ResponseWriter, r *http.Request) {
	if !s.cabinetOK() {
		http.NotFound(w, r)
		return
	}
	out := map[string]any{
		"bot_username": s.mini.CabinetBotUsername(),
		// mail_on гасит на входе и «забыли пароль», и обещание письма при
		// регистрации: предлагать действие, которое заведомо не сработает,
		// хуже, чем не предлагать его вовсе.
		"mail_on": s.mini.MailReady(),
	}
	// client_id есть — страница ставит кнопку новой библиотеки входа Telegram,
	// нет — прежний виджет по bot_username. Сервер принимает оба вида входа.
	if s.mini.CabinetTGLoginNew() {
		out["client_id"] = tgBotID(s.mini.MiniBotToken())
	}
	// Вошедшему чат приходит вместе с меню; сюда он попадает, только когда
	// его разрешено показывать и на экране входа.
	if src, all := s.mini.CabinetChat(); src != "" && all {
		out["chat_src"] = src
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCabinetTelegramAuth exchanges a Telegram login (id_token or legacy widget data) for a token.
func (s *Server) handleCabinetTelegramAuth(w http.ResponseWriter, r *http.Request) {
	if !s.cabinetOK() {
		http.NotFound(w, r)
		return
	}

	body, err := readAllLimited(r, 16*1024)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req struct {
		tgLoginReq
		// Start — параметр ссылки, по которой открыли кабинет (ref_, inv_).
		// В подпись виджета не входит, поэтому доверия к нему не больше, чем к
		// /start в чате: действует только для нового аккаунта и по тем же
		// правилам.
		Start string `json:"start"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	u, err := s.cabinetTGUser(req.tgLoginReq)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "не удалось проверить вход через Telegram"})
		return
	}
	tgID := u.ID
	// До заведения пользователя: привязка к пригласившему действует только
	// для новичка.
	if st := strings.TrimSpace(req.Start); st != "" && len(st) <= 64 {
		s.mini.MiniStart(ctx, tgID, st, true)
	}
	s.mini.CabinetEnsureUser(ctx, tgID)
	s.mini.CabinetBindName(ctx, tgID, u.Username, u.FirstName)
	if err := s.mini.CabinetGate(ctx, tgID, false); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	s.issueCabinetToken(ctx, w, tgID)
}

func (s *Server) cabinetEmail(w http.ResponseWriter, r *http.Request, register bool) {
	if !s.cabinetOK() {
		http.NotFound(w, r)
		return
	}
	body, err := readAllLimited(r, 8*1024)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		// Start — параметр ссылки, по которой открыли кабинет: действует
		// только при заведении нового аккаунта, как /start в чате.
		Start string `json:"start"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	var id int64
	if register {
		st := strings.TrimSpace(req.Start)
		if len(st) > 64 {
			st = ""
		}
		id, err = s.mini.CabinetEmailRegister(ctx, req.Email, req.Password, st)
	} else {
		id, err = s.mini.CabinetEmailLogin(ctx, req.Email, req.Password)
	}
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	s.issueCabinetToken(ctx, w, id)
}

func (s *Server) handleCabinetRegister(w http.ResponseWriter, r *http.Request) {
	s.cabinetEmail(w, r, true)
}
func (s *Server) handleCabinetLogin(w http.ResponseWriter, r *http.Request) {
	s.cabinetEmail(w, r, false)
}

// handleCabinetP2PScreenshot accepts a payment screenshot uploaded from the web
// cabinet (multipart) and forwards it to the admin for approval.
func (s *Server) handleCabinetP2PScreenshot(w http.ResponseWriter, r *http.Request) {
	// Чек принимается и из мини-аппа: заявка сверяется с владельцем пропуска
	// (CabinetP2PScreenshot), чужую заявку так не закрыть.
	id, _, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	// Дедлайн записи для этой одной ручки. Общий WriteTimeout отсчитывается от
	// РАЗБОРА ЗАГОЛОВКОВ, а не от конца тела: чек до 12 МБ с мобильного
	// аплинка заливается дольше, чем весь бюджет, и ответ покупателю уже не
	// уходит — админ чек получил, а человек видит ошибку сети и жмёт ещё раз.
	// Ошибку игнорируем намеренно: на HTTP/2 управление дедлайном не
	// поддерживается, поведение просто останется прежним.
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Now().Add(90 * time.Second))
	}
	// Hard-cap the whole request body so an oversized upload can't spool large
	// temp files to disk (ParseMultipartForm's argument is only the in-memory
	// threshold, not a total limit).
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	reqID, _ := strconv.ParseInt(r.FormValue("req_id"), 10, 64)
	file, hdr, err := r.FormFile("photo")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "файл не загружен"})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 8<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.mini.CabinetP2PScreenshot(ctx, id, reqID, hdr.Filename, data); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCabinetStatic serves the cabinet SPA at the live-configured path. It is
// registered as the catch-all so the path can change without a restart.
func (s *Server) handleCabinetStatic(w http.ResponseWriter, r *http.Request) {
	if !s.cabinetOK() {
		http.NotFound(w, r)
		return
	}
	p := s.mini.CabinetPath()
	if !strings.HasPrefix(r.URL.Path, p) {
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, p)
	if rel == "" || rel == "index.html" {
		s.serveCabinetHTML(w)
		return
	}
	fsys, err := s.staticFS()
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	http.StripPrefix(p, http.FileServer(http.FS(fsys))).ServeHTTP(w, r)
}

var fpAlphabet = []byte("abcdefghijklmnopqrstuvwxyz0123456789")

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Cosmetic (anti-fingerprint) use only; on the rare read error fall back
		// to a deterministic pad rather than failing the request.
		for i := range b {
			b[i] = fpAlphabet[i%len(fpAlphabet)]
		}
		return string(b)
	}
	for i := range b {
		b[i] = fpAlphabet[int(b[i])%len(fpAlphabet)]
	}
	return string(b)
}

// cabinetTitleHTML — заголовок в виде, пригодном и для разметки, и для строки
// скрипта в кавычках: классическая страница держит его и там, и там. Всё, что
// значимо в разметке или в такой строке, записано HTML-сущностями.
func cabinetTitleHTML(title string) string {
	return strings.ReplaceAll(html.EscapeString(title), `\`, "&#92;")
}

// serveCabinetHTML serves the SPA with the configured title/description/favicon
// injected, and (in anti-fingerprint mode) randomized markers so the page is
// harder to identify as this bot's cabinet.
func (s *Server) serveCabinetHTML(w http.ResponseWriter) {
	ui := s.webUI("cabinet")
	data, err := s.readIndexHTML("cabinet.html", ui.Design)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	antifp := s.mini.CabinetAntiFP()
	branded := hasBrandMarker(data)

	// Заголовок кабинета — и название вкладки (<title>), и видимый заголовок
	// страницы. Описание — только тег <meta>.
	title := strings.Join(strings.Fields(s.mini.CabinetTitle()), " ")
	own := title != ""
	if !own {
		title = "Личный кабинет"
		if antifp {
			title = randToken(6)
		}
	}

	head := ""
	if d := s.mini.CabinetDescription(); d != "" {
		head += "<meta name=\"description\" content=\"" + html.EscapeString(d) + "\">\n"
	}
	fav := s.mini.CabinetFavicon()
	if fav == "" && branded {
		// Без отдельного фавикона вкладку подписывает логотип сервиса.
		fav = ui.Logo
	}
	if fav != "" {
		head += "<link rel=\"icon\" href=\"" + html.EscapeString(fav) + "\">\n"
	}
	if antifp {
		// vary the served bytes per request to avoid a static byte-for-byte fingerprint
		head += "<!-- " + randToken(16) + " -->\n"
	}

	// Места подстановки сначала отмечаются в исходной странице, потом все
	// значения ставятся одним проходом. Подставленное повторно не
	// просматривается: свой заголовок со словами «Личный кабинет» не
	// задваивается, а текст из настроек не принимается за метку страницы.
	// Метки — с нулевым байтом: в странице его нет.
	const markHead, markTitle, markHeading, markBrand = "\x00head\x00", "\x00title\x00", "\x00heading\x00", "\x00brand\x00"
	out := strings.ReplaceAll(string(data), "\x00", "")
	out = strings.Replace(out, "<title>", markHead+"<title>", 1)
	// «Кабинет» — прежняя метка: она в своих страницах операторов, сделанных
	// из старой копии встроенной.
	for _, name := range []string{"Личный кабинет", "Кабинет"} {
		out = strings.Replace(out, "<title>"+name+"</title>", "<title>"+markTitle+"</title>", 1)
	}
	values := []string{markHead, head, markTitle, cabinetTitleHTML(title)}
	if branded {
		// Страница нового образца берёт заголовок из подставленных настроек:
		// замена текста по всей разметке задела бы строки внутри скрипта.
		if own || antifp {
			ui.Title = title
		}
		if js, err := json.Marshal(ui); err == nil {
			out = strings.Replace(out, brandMarker, markBrand, 1)
			values = append(values, markBrand, string(js))
		}
	} else {
		// Видимый заголовок (экран входа и шапка) — всегда из настроек.
		out = strings.ReplaceAll(out, "Личный кабинет", markHeading)
		values = append(values, markHeading, cabinetTitleHTML(title))
	}
	out = strings.NewReplacer(values...).Replace(out)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if antifp {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	_, _ = w.Write([]byte(out))
}

// --- аккаунт кабинета: почта, пароль, привязка Telegram ---

// cabinetJSON читает тело запроса кабинета. Тела здесь короткие (адрес,
// пароль, значение ссылки), поэтому предел жёсткий.
func cabinetJSON(r *http.Request, dst any) bool {
	body, err := readAllLimited(r, 8*1024)
	if err != nil {
		return false
	}
	return json.Unmarshal(body, dst) == nil
}

// handleCabinetAccount отдаёт состояние аккаунта: чем человек входит, что
// подтверждено и что из-за этого закрыто.
func (s *Server) handleCabinetAccount(w http.ResponseWriter, r *http.Request) {
	id, web, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	if !web {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "только для веб-кабинета"})
		return
	}
	writeJSON(w, http.StatusOK, s.mini.CabinetAccount(r.Context(), id))
}

// handleCabinetVerifySend отправляет письмо с подтверждением ещё раз.
func (s *Server) handleCabinetVerifySend(w http.ResponseWriter, r *http.Request) {
	id, web, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	if !web {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "только для веб-кабинета"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.mini.CabinetSendVerify(ctx, id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCabinetVerify гасит ссылку из письма.
//
// Ручка намеренно POST и намеренно без авторизации: письмо открывают в чужом
// браузере и на телефоне, а GET-ссылку на изменение состояния выбирают за
// человека почтовые антивирусы, гася её до того, как он до неё доберётся.
func (s *Server) handleCabinetVerify(w http.ResponseWriter, r *http.Request) {
	if !s.cabinetOK() {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !cabinetJSON(r, &req) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.mini.CabinetVerifyEmail(ctx, req.Token); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCabinetPasswordChange меняет пароль по старому и выдаёт свежий пропуск
// взамен своего же: поколение поднялось, и прежний пропуск этой вкладки умер
// вместе с чужими.
func (s *Server) handleCabinetPasswordChange(w http.ResponseWriter, r *http.Request) {
	id, web, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	if !web {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "только для веб-кабинета"})
		return
	}
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if !cabinetJSON(r, &req) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.mini.CabinetChangePassword(ctx, id, req.Old, req.New); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.issueCabinetToken(ctx, w, id)
}

// handleCabinetForgot принимает адрес и отвечает одинаково всегда: иначе форма
// становится проверялкой «есть ли у вас аккаунт с таким адресом».
func (s *Server) handleCabinetForgot(w http.ResponseWriter, r *http.Request) {
	if !s.cabinetOK() {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if !cabinetJSON(r, &req) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	s.mini.CabinetForgotPassword(ctx, req.Email)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCabinetReset ставит новый пароль по ссылке из письма.
func (s *Server) handleCabinetReset(w http.ResponseWriter, r *http.Request) {
	if !s.cabinetOK() {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !cabinetJSON(r, &req) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	id, err := s.mini.CabinetResetPassword(ctx, req.Token, req.Password)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.issueCabinetToken(ctx, w, id)
}

// handleCabinetBind привязывает Telegram к аккаунту, заведённому по почте.
// Подпись виджета входа проверяется тем же кодом, что и на входе: привязка —
// такое же доказательство владения аккаунтом Telegram, как и вход им.
func (s *Server) handleCabinetBind(w http.ResponseWriter, r *http.Request) {
	id, web, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	if !web {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "только для веб-кабинета"})
		return
	}
	var req tgLoginReq
	if !cabinetJSON(r, &req) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	u, err := s.cabinetTGUser(req)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "не удалось проверить вход через Telegram"})
		return
	}
	bound, err := s.mini.CabinetBindTelegram(ctx, id, u.ID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.mini.CabinetBindName(ctx, bound, u.Username, u.FirstName)
	s.issueCabinetToken(ctx, w, bound)
}
