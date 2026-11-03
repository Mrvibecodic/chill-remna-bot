package app

import (
	"bytes"
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Отдельный бот для бэкапов: файлы шлёт он, и чат с этим ботом не
// засоряется. Сам этот бот другому боту файл передать не может — Telegram не
// даёт ботам писать ботам, — поэтому нужен токен второго бота.

// fileUploadTimeout — сколько ждём загрузку файла до 50 МБ.
const fileUploadTimeout = 5 * time.Minute

var botTokenRe = regexp.MustCompile(`^[0-9]{5,}:[A-Za-z0-9_-]{30,}$`)

// backupSender шлёт бэкапы токеном отдельного бота.
type backupSender interface {
	// Check проверяет токен и отдаёт @username бота.
	Check(ctx context.Context) (string, error)
	SendText(ctx context.Context, chatID int64, text string) error
	SendFile(ctx context.Context, chatID int64, name string, data []byte, caption string) error
}

type tgBackupSender struct {
	b     *bot.Bot
	token string
}

func newTGBackupSender(token string, opts ...bot.Option) (backupSender, error) {
	opts = append([]bot.Option{bot.WithSkipGetMe(),
		bot.WithHTTPClient(time.Minute, &http.Client{Timeout: fileUploadTimeout})}, opts...)
	b, err := bot.New(token, opts...)
	if err != nil {
		return nil, redactToken(err, token)
	}
	return &tgBackupSender{b: b, token: token}, nil
}

// backupSenderFor — отправитель для токена (в тестах подменяется).
func (a *App) backupSenderFor(token string) (backupSender, error) {
	if a.newBackupSender != nil {
		return a.newBackupSender(token)
	}
	return newTGBackupSender(token)
}

func (s *tgBackupSender) Check(ctx context.Context) (string, error) {
	me, err := s.b.GetMe(ctx)
	if err != nil {
		return "", redactToken(err, s.token)
	}
	return me.Username, nil
}

func (s *tgBackupSender) SendText(ctx context.Context, chatID int64, text string) error {
	_, err := s.b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, ParseMode: models.ParseModeHTML})
	return redactToken(err, s.token)
}

func (s *tgBackupSender) SendFile(ctx context.Context, chatID int64, name string, data []byte, caption string) error {
	_, err := s.b.SendDocument(ctx, &bot.SendDocumentParams{
		ChatID:    chatID,
		Document:  &models.InputFileUpload{Filename: name, Data: bytes.NewReader(data)},
		Caption:   caption,
		ParseMode: models.ParseModeHTML,
	})
	return redactToken(err, s.token)
}

// redactToken прячет токен в тексте ошибки: он уходит и в лог, и админу.
// Цепочка ошибок сохраняется — по ней видно, например, «бот заблокирован».
func redactToken(err error, token string) error {
	if err == nil || token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return &textErr{msg: strings.ReplaceAll(err.Error(), token, "***"), err: err}
}

// textErr — ошибка со своим текстом для человека поверх исходной: по
// цепочке видно, что случилось на самом деле.
type textErr struct {
	msg string
	err error
}

func (e *textErr) Error() string { return e.msg }
func (e *textErr) Unwrap() error { return e.err }
