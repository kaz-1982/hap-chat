# テスト

3 層に分けています。下にいくほど遅く、しかし本物に近くなります。

| 層 | 場所 | 走らせ方 | 必要なもの | 目安 |
|---|---|---|---|---|
| 単体 | `internal/*/**_test.go` | `go test ./...` | なし | 1 秒未満 |
| 結合 | `internal/store` `internal/web` | `go test ./...` | MySQL（compose） | 数秒 |
| E2E | `e2e/tests/` | `cd e2e && npm test` | MySQL + Chromium | 15 秒前後 |

```bash
docker compose up -d     # 結合と E2E に必要
go test ./...            # 単体 + 結合
cd e2e && npm test       # E2E
```

MySQL が動いていなければ結合テストは自動的に skip されます（`internal/testdb`）。
単体テストだけを走らせたいときは `go test -run 'Test[^S]' ./internal/chat/` のように絞るか、
Docker を止めておけば結合分は skip されます。

---

## 単体テスト

DB もネットワークも使いません。対象は「純粋な関数」と「時間に依存するロジック」です。

| ファイル | 守っているもの |
|---|---|
| `internal/web/templates_test.go` | `richText` のエスケープ。`<script>`、属性を閉じる引用符、コードスパンの中身 |
| `internal/web/session_test.go` | Cookie の署名検証、鍵が違えば通らないこと、マルチバイトを割らない `truncate` |
| `internal/chat/hub_test.go` | 入力中の TTL、退室通知の猶予、部屋をまたがない配信、詰まった接続の読み飛ばし |

### `testing/synctest` で時間を早送りする

Hub には「4 秒で入力中が消える」「3 秒待ってから退室を知らせる」といった時間依存の仕様があります。
そのまま書くと 1 本で数秒かかりますが、Go 1.25 以降の `testing/synctest` を使うと
**バブルの中の時計が偽物になる**ので、実時間を待たずに検証できます。

```go
synctest.Test(t, func(t *testing.T) {
    h, _ := newTestHub(t)
    h.Typing(1, bob)

    time.Sleep(typingTTL + time.Second) // 実時間は 0 秒
    synctest.Wait()                     // 他の goroutine が落ち着くまで待つ

    if got := h.TypingUsers(1); len(got) != 0 {
        t.Fatalf("TTL 後も残っている: %d 人", len(got))
    }
})
```

バブルは中の goroutine が全部終わるまで待つので、`Hub` の掃除役（janitor）は
`ctx` で止められるようにしてあります（`NewHub(ctx, st)`）。止まらない goroutine があると
テストがデッドロック扱いになります。

### DB なしで Hub を試すための `chat.Store`

Hub は永続化を直接は知りません。必要な 4 つの操作だけをインターフェースにしてあります。

```go
type Store interface {
    Rooms(ctx context.Context) ([]model.Room, error)
    EnsureWelcome(ctx context.Context, roomID uint, text string) error
    Recent(ctx context.Context, roomID uint, limit int) ([]model.Message, error)
    Add(ctx context.Context, m *model.Message) error
}
```

本番では `*store.Store` が入り、テストではメモリ上の `fakeStore` が入ります。

---

## 結合テスト

本物の MySQL と `httptest.Server` を使って、HTTP から DB までを通します。

`internal/testdb` がテスト用データベースを用意します。パッケージごとに
`hapchat_test_store` / `hapchat_test_web` と名前を分けているのは、
Go がパッケージのテストを並行に走らせるためです。中身は毎回空にします。

### 何を確かめているか

- **`internal/store`** — 初期ルームの順序、`EnsureWelcome` が重複しないこと、
  `Recent` が古い順で返すこと、`Preload("User")` で投稿者が引けること、
  絵文字（4 バイト）の往復、`ON DELETE SET NULL` と `ON DELETE CASCADE` の挙動
- **`internal/web`** — 入室と Cookie 発行、署名の改ざん拒否、投稿が 204 で本文を返さないこと、
  HTML エスケープが最後まで効いていること、ルーム切り替えの断片と `HX-Push-Url`

### SSE をテストする

`internal/web/server_test.go` に小さな SSE クライアントを書いてあります。
`event:` と `data:` を読んでイベントに組み立て、チャネルに流すだけのものです。

これで「A の発言が B に届き、しかも `is-mine` が付くのは A 側だけ」という、
**受信者ごとに描画している**中核の仕様を実際に確かめられます。

```go
alice.form("/r/general/messages", url.Values{"text": {"やっほー"}})

own := aStream.await(t, "message", "やっほー")     // 送信者
other := bStream.await(t, "message", "やっほー")   // 受信者
// own には is-mine が付き、other には付かない
```

---

## E2E（Playwright）

**このアプリで実際に踏んだバグは、ほとんどがこの層でした。** 単体にも結合にも出てきません。

```bash
cd e2e
npm install
npx playwright install chromium
npm test              # ヘッドレス
npm run test:headed   # ブラウザを見ながら
npm run report        # 失敗時のレポート
```

`e2e/run-server.sh` が毎回テスト用データベースを作り直し、`go build` したバイナリを
`127.0.0.1:8123` で起動します。embed 済みのバイナリで確かめるので、
本番と同じ形（テンプレートも静的ファイルもバイナリの中）でのテストになります。

### 回帰テストとして残しているバグ

| テスト | 元のバグ |
|---|---|
| `Enter キーで送信できる` | Alpine の `$el` がハンドラを書いた要素（input）を指しており、`requestSubmit()` が呼べていなかった |
| `入力中の通知が下書きを消さない` | 入力欄の `hx-post` が発火させた `htmx:afterRequest` がフォームにもバブリングし、下書きが消えていた |
| `切り替えると URL と内容が変わり、発言が混ざらない` | ルーム切り替え直後は htmx がまだフォームを処理しておらず、Enter がネイティブ送信になって `/r/dev?text=...` へ GET 遷移し、発言が消えていた |
| `再読み込みしても最新の発言が見えている` | CSS の `scroll-behavior: smooth` のせいで `scrollTop` の代入がアニメーションになり、初期表示が最上部で止まっていた |
| `テーマの切り替えがリロード後も残る` | localStorage への保存と、描画前の適用 |
| `切り替え後も SSE が繋がっている` | 新しい接続が開いたあとに古い接続の `htmx:sseClose` が届き、繋がっているのに切断表示になっていた |

さらに E2E がフレーキーになったことをきっかけに、**購読より先に投稿された発言が
画面に出ない**という穴も見つかりました。ルーム切り替え直後は
「パネルの描画 → SSE 接続」の間に隙間があり、そこに自分の投稿が挟まると
保存はされるのに配信先が居ない、という状態になります。

直し方は、描画済みの最後の ID を接続時に渡して、サーバー側でその後の分を送り直すことです。

```html
sse-connect="/r/{{.Room.Slug}}/sse?after={{.LastID}}"
```

購読してから取りこぼし分を送り、送った ID 以下は配信ループ側で読み飛ばして重複を防ぎます
（`internal/web/server.go` の `handleSSE`、`store.Since`）。

3 つ目は E2E を書いていて初めて見つかったものです。直し方は、フォームに
`action` と `method="post"` を書いて、**htmx が間に合わなくてもネイティブ送信が同じ POST になる**
ようにしました（プログレッシブ・エンハンスメント）。

### テストを書くときの注意

- **`pressSequentially` で日本語を打っても `keyup` は飛びません。** Playwright が
  `insertText` を使うためです。この件をきっかけに、入力中通知の `hx-trigger` は
  `keyup` から `input` に変えました（貼り付けにも反応するようになり、こちらのほうが素直です）。
- **色見本の `<input type=radio>` は視覚的に隠しています。** ラベルをクリックしてください
  （人間の操作と同じ）。
- **同じデータベースを共有するので直列に走らせています**（`workers: 1`）。
  発言はテスト間で溜まっていくので、件数で数える assertion（`toHaveCount(1)` など）は書かず、
  `unique()` で一意にした本文でカードを絞り込んでから可視/不可視を見ます。
- **`send()` は「画面に出る」ところまで確かめます。** 入力欄が空になるだけでは
  送信できた証拠になりません（下書きは楽観的に消しているため）。

---

## CI（GitHub Actions）

[.github/workflows/ci.yml](../.github/workflows/ci.yml) で、push と pull request のたびに 2 つのジョブが走ります。

| ジョブ | 中身 |
|---|---|
| `go` | `gofmt` の確認 → `go vet` → `go build` → **`go test -race ./...`** |
| `e2e` | Chromium を入れて `npm test`。失敗したら Playwright のレポートを成果物として残す |

どちらのジョブにも `services: mysql`（mysql:8.4）を付けてあり、
`mysqladmin ping` のヘルスチェックが通ってからテストが始まります。
接続先はワークフローの環境変数で渡しています。

```yaml
env:
  HAPCHAT_TEST_DSN: "root:root@tcp(127.0.0.1:3306)/hapchat_test?..."
  E2E_DSN:          "root:root@tcp(127.0.0.1:3306)/hapchat_e2e?..."
```

`-race` を CI で常用しているのは、Hub が goroutine 前提だからです。
配信・在室・タイマーが同じマップを触るので、競合が入り込む余地があります。

### mysql クライアントに依存させない

E2E はテストのたびにデータベースを作り直します。最初は `docker exec hapchat-mysql mysql ...`
と書いていましたが、これだと CI（サービスコンテナに名前が付かない）で動きません。
今は Go のツールに置き換えてあります。

```bash
go run ./cmd/resetdb -dsn "$E2E_DSN"
```

DSN からデータベース名を取り出して `DROP` / `CREATE` するだけの短いものです。
うっかり本番を消さないよう、アプリ本体とは別のコマンドにしてあります。

## これから足すとよいもの

| やること | ねらい |
|---|---|
| カバレッジ | `go test -coverprofile=cover.out ./... && go tool cover -html=cover.out` |
| モバイル幅の E2E | `devices['iPhone 15']` を projects に足す |
| アクセシビリティ検査 | `@axe-core/playwright` でキーボード操作とコントラストを見る |
| 依存の更新 | Dependabot で Go / npm / GitHub Actions をまとめて追う |
