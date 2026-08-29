#!/usr/bin/env bash
# Playwright が起動するテスト用サーバー。
# 毎回データベースを作り直すので、E2E は必ず空の状態から始まる。
set -euo pipefail

PORT="${E2E_PORT:-8123}"
DSN="${E2E_DSN:-root:root@tcp(127.0.0.1:3306)/hapchat_e2e?charset=utf8mb4&parseTime=True&loc=Local}"

cd "$(dirname "$0")/.."

# mysql クライアントにも docker にも依存しないよう、Go のツールで作り直す
go run ./cmd/resetdb -dsn "${DSN}"

# embed 済みのバイナリを作って動かす（本番と同じ形で確かめる）
BIN="${TMPDIR:-/tmp}/hapchat-e2e"
go build -o "${BIN}" .
exec "${BIN}" -addr "127.0.0.1:${PORT}" -dsn "${DSN}" -secret e2e-secret
