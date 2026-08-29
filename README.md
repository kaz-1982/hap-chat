# HAP Chat — htmx + Alpine.js + Pico.css のショーケース

[![CI](https://github.com/kaz-1982/hap-chat/actions/workflows/ci.yml/badge.svg)](https://github.com/kaz-1982/hap-chat/actions/workflows/ci.yml)

リアルタイムのチャットアプリです。**Pico.css の部品をひととおり使い切る**ことを目的に、
htmx（通信と DOM 差し替え）、Alpine.js（手元の UI 状態）、Go + GORM（サーバーと MySQL）で組んでいます。

```bash
docker compose up -d          # MySQL と Adminer を起動
export HAPCHAT_SECRET=dev-secret
go run . -dev                 # アプリはホストで動かす
```

→ アプリ http://localhost:8080 ／ DB 管理画面 http://localhost:8081（Adminer, サーバー `db` / ユーザー `hap` / パスワード `hap` / DB `hapchat`）

`-dev` はテンプレートと CSS をディスクから読むので、編集してリロードするだけで反映されます。
初回起動時にテーブル作成（AutoMigrate）と初期ルームの投入まで自動で行われます。

配布は 1 ファイル。テンプレートと静的ファイルは `embed` でバイナリに同梱されます。

```bash
go build -o hapchat . && ./hapchat -addr :3000
```

主なフラグ:

| フラグ / 環境変数 | 既定値 | 用途 |
|---|---|---|
| `-dsn` / `HAPCHAT_DSN` | `hap:hap@tcp(127.0.0.1:3306)/hapchat?...` | MySQL の接続先 |
| `-secret` / `HAPCHAT_SECRET` | （空＝起動ごとにランダム） | セッション Cookie の署名鍵。固定すると再起動しても入室状態が続く |
| `-db-wait` | `30s` | MySQL コンテナの起動を待つ時間 |
| `-sql` | `false` | 実行された SQL をログに出す（GORM の勉強用） |
| `-dev` | `false` | テンプレート / 静的ファイルをディスクから読む |

複数人で試すには、別のブラウザ（またはシークレットウィンドウ）で開いてください。
参加すると `users` に 1 行できて、署名付き Cookie でそのユーザーに紐づきます。

> **資格情報について**
> このリポジトリに書かれている `root:root` や `hap:hap`、`dev-secret` は、
> `127.0.0.1` にだけ公開しているローカル開発用の値です。そのまま外に出さないでください。
> 本番相当で動かすなら、`HAPCHAT_SECRET` と DSN は環境変数で渡します。

## ドキュメント

| | |
|---|---|
| [docs/architecture.md](docs/architecture.md) | 全体構成・パッケージ依存・「発言が届くまで」のシーケンス図・状態の置き場所 |
| [docs/database.md](docs/database.md) | ER 図・設計の意図・GORM の書き方と実際に流れる SQL |
| [docs/testing.md](docs/testing.md) | 単体・結合・E2E の分担、synctest の使い方、回帰テストとして残したバグ |

図は Mermaid です。VS Code の Markdown プレビューで見るには
Markdown Preview Mermaid Support 拡張を入れてください。

---

## テスト

```bash
docker compose up -d     # 結合・E2E に必要
go test ./...            # 単体 + 結合
cd e2e && npm test       # E2E（Playwright）
```

| 層 | 何を守るか |
|---|---|
| 単体 | HTML エスケープ、Cookie の署名、入力中の TTL と退室の猶予（`testing/synctest` で時間を早送り） |
| 結合 | 本物の MySQL と `httptest.Server`。SSE で 2 者間に届くこと、永続化、外部キーの挙動 |
| E2E | 本物のブラウザ。Enter 送信、ルーム切り替え、テーマの永続化など htmx / Alpine まわり |

MySQL が動いていなければ結合テストは自動で skip されます。

push と pull request のたびに [GitHub Actions](.github/workflows/ci.yml) が
`gofmt` / `go vet` / `go test -race` と Playwright を回します。
詳しくは [docs/testing.md](docs/testing.md) を見てください。

---

## データベース

MySQL 8.4（Docker）に GORM で読み書きします。テーブルは 3 つだけです。

| `users` | 型 | |
|---|---|---|
| `id` | PK | |
| `public_id` | UNIQUE | 署名付き Cookie に入る外向きの ID |
| `name` / `color` | | 表示名とアバター色 |
| `created_at` / `updated_at` | | |

| `rooms` | 型 | |
|---|---|---|
| `id` | PK | |
| `slug` | UNIQUE | URL に出る名前（`/r/general`） |
| `name` / `icon` / `topic` / `position` | | 表示用 |

| `messages` | 型 | |
|---|---|---|
| `id` | PK | |
| `room_id` | FK → `rooms` | `ON DELETE CASCADE` |
| `user_id` | FK → `users`, NULL 可 | `ON DELETE SET NULL` |
| `kind` | `chat` / `system` | 入退室のお知らせは `system` |
| `text` | TEXT | |
| `created_at` | | `INDEX(room_id, created_at)` |

- **入退室のお知らせ**は `kind = "system"`、`user_id` は NULL です。ユーザーを消しても
  `ON DELETE SET NULL` で発言だけ残ります。
- **在室状況と「入力中…」は DB に入れません**。あれは接続の状態であってデータではないので、
  メモリ（`internal/chat/hub.go`）に置いたままです。再起動すれば全員いなくなるのが正しい。
- 画面に読み込むのは直近 200 件（`chat.HistoryLimit`）。DB にはすべて残ります。
- マイグレーションは GORM の `AutoMigrate`。初期ルームは slug が無いときだけ作られるので、
  トピックを DB で書き換えても起動時に戻されません。
- 日本語と絵文字のため、DB は `utf8mb4` / `utf8mb4_0900_ai_ci` で作成しています。
- **MySQL と Adminer は `127.0.0.1` にだけ公開しています。** compose で `"3306:3306"` と書くと
  `0.0.0.0` に開いてしまい、同じ Wi-Fi にいる誰でも `hap/hap` で DB に入れます。
  ホストの IP を明示した `"127.0.0.1:3306:3306"` が正解です。
  なお `go run .` 側は `:8080`（全インターフェース）で待ち受けます。スマホから試すには便利ですが、
  外に出したくないときは `-addr 127.0.0.1:8080` にしてください。

セッション Cookie には `users.public_id` と、その **HMAC-SHA256 署名**だけを入れています。
名前も色も DB 側が正なので、Cookie を書き換えても他人にはなりすませません
（`internal/web/server.go` の `sign` / `currentUser`）。

## なぜバックエンドが Go 標準ライブラリなのか

htmx は「サーバーが HTML を返す」前提の道具なので、**テンプレートエンジンが素直で、
SSE のようなストリーミングを止めない**フレームワークが相性のいい相手になります。
Go の `net/http` + `html/template` はその条件をどちらも満たします。

| | 理由 |
|---|---|
| 依存が少ない | 追加したのは GORM と MySQL ドライバだけ。壊れる要素が少なく、学習の邪魔をしません |
| `html/template` | 文脈を見て自動エスケープする。htmx の断片を返すのに過不足がない |
| SSE がそのまま書ける | `http.ResponseController.Flush()` だけでストリーミングできる |
| goroutine + channel | 「接続ごとに 1 本」というブロードキャストの実装が素直に書ける |
| 単一バイナリ | `embed` で HTML/CSS/JS ごと固められる |

他の選択肢も相性は良いです。好みで置き換えられます。

- **Python + FastAPI / Flask + Jinja2** — テンプレートが強力。SSE は `StreamingResponse` で書けます
- **Node + Hono / Fastify** — フロントと言語を揃えたいとき
- **Ruby on Rails / Laravel** — 既に慣れているなら、htmx は素の HTML に足すだけなので導入しやすい

この題材（チャット）に限れば、常時接続を大量に抱えるので Go か Elixir が一番素直です。

---

## 役割分担

```
ブラウザ                                     サーバー (Go)
┌────────────────────────────┐               ┌─────────────────────────┐
│ htmx                       │  POST 送信 →  │ handler → Hub.Post()    │
│  - フォーム送信             │               │                         │
│  - ルーム切り替え           │  ← SSE 配信   │ Hub → 各購読者へ配信     │
│  - SSE 受信 (sse-swap)      │               │ 受信者ごとに HTML を描画 │
├────────────────────────────┤               └─────────────────────────┘
│ Alpine.js                  │
│  - 設定（テーマ・文字サイズ）│  ← サーバーに送らない状態はここ
│  - 下書き・絵文字・絞り込み  │
├────────────────────────────┤
│ Pico.css                   │  ← クラスをほとんど書かずに見た目が決まる
└────────────────────────────┘
```

要点は **「サーバーは HTML を返す。JSON を返してクライアントで組み立て直さない」** ことです。
新しい発言は SSE で HTML 断片として届き、htmx が `beforeend` で差し込みます。

受信者ごとにテンプレートを描画しているので、「自分の発言」の見た目や
「自分の入力中は表示しない」といった判断をサーバー側で書けます
（`internal/web/templates.go` の `renderEvent`）。

---

## ディレクトリ

```
.github/workflows/ci.yml      CI（Go の単体・結合と、Playwright の E2E）
docker-compose.yml            MySQL 8.4 + Adminer
main.go                       起動・DB 接続・embed・シグナル処理
cmd/resetdb/                  E2E 用にデータベースを作り直す開発ツール
internal/model/model.go       GORM のモデル（User / Room / Message）
internal/store/store.go       MySQL への読み書き・マイグレーション・初期データ
internal/chat/hub.go          配信 / 在室 / 入力中（接続に紐づく状態だけ）
internal/web/server.go        ルーティング・署名付きセッション・SSE
internal/web/templates.go     テンプレート読み込みと本文の装飾（`code` / URL / @メンション）
templates/                    ページと断片（断片は SSE でもそのまま使う）
static/css/app.css            アプリの骨格だけを書いた薄い CSS
static/js/app.js              Alpine のストア・コンポーネントと htmx との糊
static/vendor/                pico 2.1.1 / htmx 2.0.7 / htmx-ext-sse 2.2.4 / alpine 3.15.0
e2e/                          Playwright の設定とテスト
docs/                         設計ノート（アーキテクチャ / DB / テスト）
```

CDN ではなく `static/vendor/` に置いてあるので、オフラインでも動き、
バージョンが勝手に変わることもありません。

---

## Pico.css のどこを使っているか

| 部品 | 使っている場所 |
|---|---|
| クラスレスの基本スタイル | 全体（`<article>` `<table>` `<form>` などをそのまま使用） |
| `<dialog>` モーダル | 設定 / プロフィール（`modal-is-open` などのクラス操作は `app.js`） |
| `<details class="dropdown">` | ルーム切り替え・ユーザーメニュー・絵文字ピッカー |
| `<details>` アコーディオン | サイドバーの「オンライン」「ショートカット」 |
| `role="group"` | 入力欄と送信ボタン |
| `role="search"` | メッセージの絞り込み |
| `aria-busy` | 送信中・参加中のローディング表示 |
| `<progress>` | 「入力中…」のインジケータ |
| `data-tooltip` / `data-placement` | ヘッダーのボタン・色見本・発言時刻 |
| `role="switch"` | テーマ切り替え、コンパクト表示などの設定 |
| `<input type="range">` | 文字サイズ |
| `<fieldset>` + ラジオ | 送信キーの選択 |
| `<table class="striped">` | 在室ユーザー一覧 |
| `data-theme` | ライト / ダーク / 端末に合わせる |
| カラーユーティリティ | アバターの色（`pico-background-azure-550` など） |
| `<kbd>` `<mark>` `<code>` `<blockquote>` `<hgroup>` `<ins>` | ショートカット表示・本文装飾・見出し |

`aria-invalid`、`overflow-auto`、`outline` / `secondary` / `contrast` のボタン差分も使っています。

---

## 覚えておくと拡張しやすい点

- **プロセスを増やすと SSE が分断されます**。発言は DB に入るので消えませんが、
  別プロセスの利用者にはリアルタイムに届きません。横に広げるなら Redis Pub/Sub などで
  `Hub.broadcast` を各プロセスへ配る必要があります。
- **名前を変えると過去の発言の表示名も変わります**。`messages` は `user_id` を持つだけなので、
  当時の名前を残したければ発言側にも名前をコピーして持たせます（意図的にそうしていません）。
- **AutoMigrate は列の削除をしません**。実運用では golang-migrate や Atlas のような
  マイグレーションツールに切り替えるのが定石です。
- **認証はありません**。名前を入れれば誰でも新しいユーザーになります。
- データを全部消してやり直すには `docker compose down -v` です。
- リロードやルーム移動で「退室 → 参加」が続けて出ないよう、退室通知は 3 秒待ってから出しています
  （`leaveGrace`）。

## ハマりどころのメモ

- htmx のイベントは**バブリング**します。入力欄の「入力中」通知（`hx-post`）が
  親フォームの `htmx:afterRequest` にも届くので、Alpine 側は `.self` で自分の分だけ拾っています。
- Alpine の `$el` は**その式を書いた要素**を指します。`@keydown` を input に書いた場合、
  フォームを取りたければ `$root` を使います。
- `scroll-behavior: smooth` を CSS に書くと `scrollTop` の代入もアニメーションになり、
  初期表示の「一番下へ」が効かなくなります。スムーススクロールは JS 側で明示しています。
- Pico のツールチップは疑似要素が常に配置されるため、スクロール領域の中で使うと
  横スクロールが生まれます。`overflow-x: hidden` と `data-placement="left"` で回避しています。
- GORM で関連（`Room` / `User`）を持つ構造体をそのまま `Create` すると、関連まで
  INSERT / UPDATE しにいきます。発言の保存では `Omit(clause.Associations)` を付けています。
- MySQL の `utf8mb4` を指定しないと絵文字（4 バイト）が保存できません。
  DSN の `charset=utf8mb4` とサーバー側の `--character-set-server=utf8mb4` の両方が必要です。

---

## ライセンス

このリポジトリのコードは MIT ライセンスです（[LICENSE](LICENSE)）。

`static/vendor/` に同梱している Pico CSS・htmx・Alpine.js は、それぞれの配布条件に従います。
内訳と原文は [static/vendor/LICENSES.md](static/vendor/LICENSES.md) にまとめてあります。
