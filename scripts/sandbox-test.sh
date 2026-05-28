#!/usr/bin/env bash
# Надёжный прогон проверок проекта в песочнице Cowork (ограниченный диск ~600 МБ).
#
# Почему так:
#   • Go-кэш НЕЛЬЗЯ класть на virtiofs-маунт (папка проекта/outputs): там запрещён
#     unlink, и `go` падает на удалении .partial при скачивании модулей.
#   • Корневой / почти полон (OS в /usr). Поэтому кэши держим в tmpfs /dev/shm
#     (есть unlink, быстро, очищается сам между вызовами).
#   • Полная сборка с modernc/sqlite (internal/storage-тесты, cmd/bot) НЕ влезает
#     по диску — её и контракт-тесты против SQLite+PostgreSQL гоняет CI
#     (.github/workflows/test.yml: postgres:17-alpine + `go test ./...`).
#
# Использование:  bash scripts/sandbox-test.sh
set -uo pipefail

GOROOT=/tmp/go
if [ ! -x "$GOROOT/bin/go" ]; then
  echo "→ Go не найден в /tmp/go — качаю go1.23.4…"
  V=go1.23.4.linux-amd64.tar.gz
  if curl -fsSL "https://go.dev/dl/$V" -o "/tmp/$V"; then
    rm -rf /tmp/go && tar -C /tmp -xzf "/tmp/$V" && rm -f "/tmp/$V"
  else
    echo "❌ Не удалось скачать Go. Установите toolchain в /tmp/go вручную." ; exit 1
  fi
fi
export GOROOT PATH="$GOROOT/bin:$PATH" GOTOOLCHAIN=local GOFLAGS=-mod=mod
export GOMODCACHE=/dev/shm/gomod GOCACHE=/dev/shm/gocache TMPDIR=/dev/shm/gotmp GOTMPDIR=/dev/shm/gotmp
mkdir -p "$GOMODCACHE" "$GOCACHE" "$GOTMPDIR"

cd "$(dirname "$0")/.." || exit 1

# Лёгкие пакеты = всё, кроме тех, что тянут драйвер modernc/sqlite.
LIGHT="./internal/app/ ./internal/web/ ./internal/yookassa/ ./internal/cryptobot/ ./internal/i18n/ ./internal/model/ ./internal/remnawave/ ./internal/crypto/ ./internal/config/ ./internal/hostctl/"

rc=0
echo "== gofmt =="
FMT=$(gofmt -l . 2>&1)
if [ -n "$FMT" ]; then echo "НЕ отформатированы:"; echo "$FMT"; rc=1; else echo "ok (clean)"; fi

echo "== go vet (light) =="
go vet $LIGHT 2>&1 | tail -20; [ ${PIPESTATUS[0]} -eq 0 ] || rc=1

echo "== go test (light) =="
go test $LIGHT -count=1 2>&1 | tail -25; [ ${PIPESTATUS[0]} -eq 0 ] || rc=1

echo "== go build ./internal/storage/ (typecheck, без sqlite-драйвера) =="
go build ./internal/storage/ 2>&1 | tail -10; [ ${PIPESTATUS[0]} -eq 0 ] || rc=1

echo
echo "ℹ internal/storage-тесты, cmd/bot и контракт-тесты SQLite+PostgreSQL не"
echo "  помещаются в песочницу по диску — их прогоняет CI (GitHub Actions)."
if [ $rc -eq 0 ]; then echo "✅ Локальные проверки пройдены"; else echo "❌ Есть проблемы (см. выше)"; fi
exit $rc
