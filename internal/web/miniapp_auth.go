package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// errAuth is returned for any init-data / token validation failure.
var errAuth = errors.New("miniapp: unauthorized")

// validateInitData verifies Telegram Mini App init data per the official
// algorithm: secret = HMAC_SHA256("WebAppData", botToken); the data-check-string
// is every field except "hash", sorted by key as key=value joined by '\n'; the
// hex HMAC of that with secret must equal the received hash. auth_date must be
// within ttl (anti-replay). Returns the authenticated Telegram user id.
func validateInitData(initData, botToken string, ttl time.Duration) (int64, error) {
	if initData == "" || botToken == "" {
		return 0, errAuth
	}
	vals, err := url.ParseQuery(initData)
	if err != nil {
		return 0, errAuth
	}
	hash := vals.Get("hash")
	if hash == "" {
		return 0, errAuth
	}

	keys := make([]string, 0, len(vals))
	for k := range vals {
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
		b.WriteString(vals.Get(k))
	}

	secret := hmacSHA256([]byte("WebAppData"), []byte(botToken))
	want := hex.EncodeToString(hmacSHA256(secret, []byte(b.String())))
	if !hmac.Equal([]byte(want), []byte(hash)) {
		return 0, errAuth
	}

	if ttl > 0 {
		ad, err := strconv.ParseInt(vals.Get("auth_date"), 10, 64)
		if err != nil || ad <= 0 {
			return 0, errAuth
		}
		// Дата «из будущего» раньше принималась без ограничений: разность
		// отрицательна, и проверка «меньше срока» проходила всегда. Перехваченная
		// подпись с большой датой жила бы вечно. Допуск в обе стороны — на
		// расхождение часов.
		if d := time.Since(time.Unix(ad, 0)); d > ttl || d < -authClockSkew {
			return 0, errAuth
		}
	}

	var u struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(vals.Get("user")), &u); err != nil || u.ID == 0 {
		return 0, errAuth
	}
	return u.ID, nil
}

func hmacSHA256(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

// --- minimal HS256 JWT (no external dependency) ---

type jwtClaims struct {
	TgID int64 `json:"tg"`
	Web  bool  `json:"w,omitempty"`
	Exp  int64 `json:"exp"`
	// Ver — поколение сессий. Ключ подписи выведен из токена бота и не меняется
	// даже при перезапуске, а в пропуске не было ни идентификатора, ни версии —
	// значит «разлогинить всех» было нечем, и украденный пропуск работал все
	// семь суток. Админ поднимает поколение — все прежние пропуска мертвы.
	Ver int `json:"v,omitempty"`
	// Epoch — поколение пропусков ОДНОГО аккаунта. Общее поколение выбивает
	// всех разом и для смены пароля не годится: сменивший пароль обязан выбить
	// чужие сессии, не трогая посторонних.
	Epoch int `json:"e,omitempty"`
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// issueJWT signs {tg,exp} with HS256 using key.
func issueJWT(tgID int64, web bool, key []byte, ttl time.Duration, ver, epoch int) string {
	header := b64url([]byte(`{"alg":"HS256","typ":"JWT"}`))
	cl, _ := json.Marshal(jwtClaims{TgID: tgID, Web: web, Exp: time.Now().Add(ttl).Unix(), Ver: ver, Epoch: epoch})
	payload := b64url(cl)
	signing := header + "." + payload
	sig := b64url(hmacSHA256(key, []byte(signing)))
	return signing + "." + sig
}

// parseJWT verifies the signature, expiry and session generation, returning the
// Telegram id. Пропуск прежнего поколения отвергается — это и есть «разлогинить
// всех».
func parseJWT(token string, key []byte, ver int) (int64, bool, int, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0, false, 0, errAuth
	}
	signing := parts[0] + "." + parts[1]
	want := b64url(hmacSHA256(key, []byte(signing)))
	if !hmac.Equal([]byte(want), []byte(parts[2])) {
		return 0, false, 0, errAuth
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, false, 0, errAuth
	}
	var cl jwtClaims
	if err := json.Unmarshal(raw, &cl); err != nil || cl.TgID == 0 {
		return 0, false, 0, errAuth
	}
	if time.Now().Unix() > cl.Exp {
		return 0, false, 0, errAuth
	}
	if cl.Ver != ver {
		return 0, false, 0, errAuth
	}
	return cl.TgID, cl.Web, cl.Epoch, nil
}

// jwtKey derives the JWT signing key from the bot token, so no extra secret
// needs to be stored or configured.
func jwtKey(botToken string) []byte {
	k := sha256.Sum256([]byte("miniapp-jwt:" + botToken))
	return k[:]
}

// authClockSkew — допустимое расхождение часов клиента и сервера при проверке
// даты подписи.
const authClockSkew = 2 * time.Minute
