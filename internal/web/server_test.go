package web_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"hapchat/internal/chat"
	"hapchat/internal/store"
	"hapchat/internal/testdb"
	"hapchat/internal/web"
)

// ---- テスト用のサーバー ----

type harness struct {
	srv *httptest.Server
	st  *store.Store
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st := testdb.New(t, "web")

	ctx, stopHub := context.WithCancel(context.Background())
	t.Cleanup(stopHub)

	hub, err := chat.NewHub(ctx, st)
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}

	root := os.DirFS("../..") // プロジェクトのルート
	assets, err := fs.Sub(root, "static")
	if err != nil {
		t.Fatal(err)
	}
	s, err := web.New(hub, st, []byte("test-secret"), assets, root, true)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return &harness{srv: srv, st: st}
}

// client は 1 人の参加者を表すブラウザ相当。
type client struct {
	t    *testing.T
	h    *harness
	http *http.Client
}

// join は入室まで済ませたクライアントを返す。
func (h *harness) join(t *testing.T, name, color string) *client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, h: h, http: &http.Client{
		Jar: jar,
		// HX-Redirect を見たいのでリダイレクトは追わない
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	resp := c.form("/join", url.Values{"name": {name}, "color": {color}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("join のステータス = %d, want 303", resp.StatusCode)
	}
	return c
}

func (c *client) form(path string, v url.Values) *http.Response {
	c.t.Helper()
	resp, err := c.http.PostForm(c.h.srv.URL+path, v)
	if err != nil {
		c.t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func (c *client) get(path string) (int, string, http.Header) {
	c.t.Helper()
	resp, err := c.http.Get(c.h.srv.URL + path)
	if err != nil {
		c.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header
}

// ---- SSE クライアント ----

type sseEvent struct {
	name string
	data string
}

type sseConn struct {
	events chan sseEvent
	stop   func()
}

// openSSE は SSE を購読して、届いたイベントをチャネルに流す。
func (c *client) openSSE(path string) *sseConn {
	c.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", c.h.srv.URL+path, nil)
	for _, ck := range c.http.Jar.Cookies(mustURL(c.h.srv.URL)) {
		req.AddCookie(ck)
	}
	resp, err := c.h.srv.Client().Do(req)
	if err != nil {
		cancel()
		c.t.Fatalf("SSE 接続: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		c.t.Fatalf("SSE のステータス = %d", resp.StatusCode)
	}

	conn := &sseConn{events: make(chan sseEvent, 64)}
	conn.stop = func() { cancel(); resp.Body.Close() }
	c.t.Cleanup(conn.stop)

	go func() {
		defer close(conn.events)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		var name string
		var data []string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = append(data, strings.TrimPrefix(line, "data: "))
			case line == "":
				if name != "" {
					select {
					case conn.events <- sseEvent{name: name, data: strings.Join(data, "\n")}:
					default:
					}
				}
				name, data = "", nil
			}
		}
	}()
	return conn
}

// await は指定した種類のイベントが来るまで待つ。
func (s *sseConn) await(t *testing.T, name string, contains string) sseEvent {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-s.events:
			if !ok {
				t.Fatalf("%s を待っている間に SSE が閉じた", name)
			}
			if e.name == name && (contains == "" || strings.Contains(e.data, contains)) {
				return e
			}
		case <-deadline:
			t.Fatalf("%s（%q を含む）が 5 秒以内に届かなかった", name, contains)
		}
	}
}

func mustURL(s string) *url.URL {
	u, _ := url.Parse(s)
	return u
}

// ---- テスト本体 ----

func TestJoinCreatesUser(t *testing.T) {
	h := newHarness(t)
	c := h.join(t, "さくら", "cyan")

	if len(c.http.Jar.Cookies(mustURL(h.srv.URL))) == 0 {
		t.Fatal("セッション Cookie が発行されていない")
	}
	status, body, _ := c.get("/r/general")
	if status != http.StatusOK {
		t.Fatalf("入室後の GET /r/general = %d", status)
	}
	if !strings.Contains(body, "さくら") {
		t.Error("ページに自分の名前が出ていない")
	}
}

func TestUnauthenticatedIsRedirected(t *testing.T) {
	h := newHarness(t)
	c := &client{t: t, h: h, http: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	status, _, hdr := c.get("/r/general")
	if status != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", status)
	}
	if got := hdr.Get("Location"); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}
}

func TestTamperedCookieIsRejected(t *testing.T) {
	h := newHarness(t)
	c := h.join(t, "さくら", "cyan")

	u := mustURL(h.srv.URL)
	orig := c.http.Jar.Cookies(u)[0]
	publicID, _, _ := strings.Cut(orig.Value, ".")

	for _, bad := range []string{
		publicID + ".AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", // 署名だけ差し替え
		publicID, // 署名なし
		"deadbeef." + strings.SplitN(orig.Value, ".", 2)[1], // 別 ID + 他人の署名
	} {
		req, _ := http.NewRequest("GET", h.srv.URL+"/r/general", nil)
		req.AddCookie(&http.Cookie{Name: orig.Name, Value: bad})
		resp, err := c.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Errorf("Cookie %q が通ってしまった（status=%d）", bad, resp.StatusCode)
		}
	}
}

func TestUnknownRoomIs404(t *testing.T) {
	h := newHarness(t)
	c := h.join(t, "さくら", "cyan")
	if status, _, _ := c.get("/r/nonexistent"); status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", status)
	}
}

// 発言は 204 で返り、本文はレスポンスに含まれない（本文は SSE で届く）。
func TestPostReturnsNoContentAndPersists(t *testing.T) {
	h := newHarness(t)
	c := h.join(t, "さくら", "cyan")

	resp := c.form("/r/general/messages", url.Values{"text": {"保存されるはず"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	if len(body) != 0 {
		t.Errorf("本文が返っている: %q", body)
	}

	_, page, _ := c.get("/r/general")
	if !strings.Contains(page, "保存されるはず") {
		t.Error("読み込み直したページに発言が出ていない")
	}
}

// 空の発言は保存しない。
func TestEmptyMessageIsIgnored(t *testing.T) {
	h := newHarness(t)
	c := h.join(t, "さくら", "cyan")
	resp := c.form("/r/general/messages", url.Values{"text": {"   "}})
	resp.Body.Close()

	rooms, _ := h.st.Rooms(context.Background())
	msgs, _ := h.st.Recent(context.Background(), rooms[0].ID, 100)
	for _, m := range msgs {
		if strings.TrimSpace(m.Text) == "" {
			t.Error("空の発言が保存されている")
		}
	}
}

// 本文の HTML はエスケープされて配信されること（XSS の最終防衛線）。
func TestMessageHTMLIsEscapedEndToEnd(t *testing.T) {
	h := newHarness(t)
	c := h.join(t, "さくら", "cyan")

	const payload = `<img src=x onerror="alert(1)">`
	c.form("/r/general/messages", url.Values{"text": {payload}}).Body.Close()

	_, page, _ := c.get("/r/general")
	if strings.Contains(page, `<img src=x`) {
		t.Error("生の HTML がページに出ている（XSS）")
	}
	if !strings.Contains(page, "&lt;img src=x") {
		t.Error("エスケープされた形が見当たらない")
	}
}

// SSE の中核：A の発言が B に届き、しかも「自分の発言」の印は受信者ごとに違う。
func TestSSEDeliversMessagePerRecipient(t *testing.T) {
	h := newHarness(t)
	alice := h.join(t, "あかり", "pink")
	bob := h.join(t, "ボブ", "jade")

	aStream := alice.openSSE("/r/general/sse")
	bStream := bob.openSSE("/r/general/sse")

	// 接続直後に在室リストが届く
	aStream.await(t, "presence", "あかり")

	alice.form("/r/general/messages", url.Values{"text": {"やっほー"}}).Body.Close()

	own := aStream.await(t, "message", "やっほー")
	if !strings.Contains(own.data, "is-mine") {
		t.Errorf("送信者に is-mine が付いていない:\n%s", own.data)
	}
	other := bStream.await(t, "message", "やっほー")
	if strings.Contains(other.data, "is-mine") {
		t.Errorf("受信者にも is-mine が付いている:\n%s", other.data)
	}
	if !strings.Contains(other.data, "あかり") {
		t.Error("受信側に投稿者名が出ていない")
	}
}

// 入室すると、既にいる人の在室リストが更新される。
func TestSSEPresenceUpdatesOnJoin(t *testing.T) {
	h := newHarness(t)
	alice := h.join(t, "あかり", "pink")
	aStream := alice.openSSE("/r/general/sse")
	aStream.await(t, "presence", "あかり")

	bob := h.join(t, "ボブ", "jade")
	bob.openSSE("/r/general/sse")

	e := aStream.await(t, "presence", "ボブ")
	if !strings.Contains(e.data, "2") {
		t.Errorf("人数が更新されていない:\n%s", e.data)
	}
}

// 入力中は本人には出さない（自分の「入力中…」が自分に見えない）。
func TestTypingNotEchoedToSelf(t *testing.T) {
	h := newHarness(t)
	alice := h.join(t, "あかり", "pink")
	bob := h.join(t, "ボブ", "jade")
	aStream := alice.openSSE("/r/general/sse")
	bStream := bob.openSSE("/r/general/sse")
	aStream.await(t, "presence", "あかり")
	bStream.await(t, "presence", "ボブ")

	alice.form("/r/general/typing", url.Values{}).Body.Close()

	// B には「あかり が入力中」が出る
	bStream.await(t, "typing", "あかり")

	// A 側には空の typing が来るだけで、自分の名前は出ない
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-aStream.events:
			if e.name == "typing" && strings.Contains(e.data, "あかり") {
				t.Fatalf("自分の入力中が自分に届いた:\n%s", e.data)
			}
			if e.name == "typing" {
				return // 空の typing が届けば期待どおり
			}
		case <-deadline:
			return
		}
	}
}

// 別の部屋の発言は届かない。
func TestSSEIsScopedToRoom(t *testing.T) {
	h := newHarness(t)
	alice := h.join(t, "あかり", "pink")
	bob := h.join(t, "ボブ", "jade")

	aStream := alice.openSSE("/r/general/sse")
	bob.openSSE("/r/dev/sse")
	aStream.await(t, "presence", "あかり")

	bob.form("/r/dev/messages", url.Values{"text": {"devの発言"}}).Body.Close()

	deadline := time.After(1500 * time.Millisecond)
	for {
		select {
		case e := <-aStream.events:
			if e.name == "message" && strings.Contains(e.data, "devの発言") {
				t.Fatal("別の部屋の発言が届いた")
			}
		case <-deadline:
			return
		}
	}
}

// SSE を張る前に投稿された分を、接続時に拾い直せること。
// ルーム切り替え直後に素早く発言すると、購読より先に投稿が届いてしまうため。
func TestSSECatchesUpMessagesPostedBeforeSubscribe(t *testing.T) {
	h := newHarness(t)
	c := h.join(t, "さくら", "cyan")

	ctx := context.Background()
	rooms, _ := h.st.Rooms(ctx)
	before, _ := h.st.Recent(ctx, rooms[0].ID, 100)
	lastID := before[len(before)-1].ID

	// SSE を張らないまま投稿する
	c.form("/r/general/messages", url.Values{"text": {"取りこぼした発言"}}).Body.Close()

	// あとから接続すると、その分が流れてくる
	stream := c.openSSE(fmt.Sprintf("/r/general/sse?after=%d", lastID))
	stream.await(t, "message", "取りこぼした発言")
}

// 拾い直した分が、そのあとの配信と重複しないこと。
func TestSSECatchUpDoesNotDuplicate(t *testing.T) {
	h := newHarness(t)
	c := h.join(t, "さくら", "cyan")

	ctx := context.Background()
	rooms, _ := h.st.Rooms(ctx)
	before, _ := h.st.Recent(ctx, rooms[0].ID, 100)
	lastID := before[len(before)-1].ID

	c.form("/r/general/messages", url.Values{"text": {"重複チェック"}}).Body.Close()
	stream := c.openSSE(fmt.Sprintf("/r/general/sse?after=%d", lastID))
	stream.await(t, "message", "重複チェック")

	// 2 通目が来ないこと（同じ本文が 2 回流れたら重複している）
	deadline := time.After(1500 * time.Millisecond)
	for {
		select {
		case e, ok := <-stream.events:
			if !ok {
				return
			}
			if e.name == "message" && strings.Contains(e.data, "重複チェック") {
				t.Fatal("同じ発言が 2 回流れた")
			}
		case <-deadline:
			return
		}
	}
}

// ルーム切り替えは断片を返し、URL の書き換えを指示する。
func TestPanelReturnsFragmentWithPushURL(t *testing.T) {
	h := newHarness(t)
	c := h.join(t, "さくら", "cyan")

	req, _ := http.NewRequest("GET", h.srv.URL+"/r/dev/panel", nil)
	req.Header.Set("HX-Request", "true")
	for _, ck := range c.http.Jar.Cookies(mustURL(h.srv.URL)) {
		req.AddCookie(ck)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if got := resp.Header.Get("HX-Push-Url"); got != "/r/dev" {
		t.Errorf("HX-Push-Url = %q, want /r/dev", got)
	}
	page := string(body)
	if strings.Contains(page, "<html") {
		t.Error("ページ全体が返っている。断片であるべき")
	}
	if !strings.Contains(page, `sse-connect="/r/dev/sse?after=`) {
		t.Error("断片に新しい SSE の接続先が入っていない")
	}
}

func TestProfileUpdateIsPersisted(t *testing.T) {
	h := newHarness(t)
	c := h.join(t, "さくら", "cyan")

	resp := c.form("/profile", url.Values{
		"name": {"さくら改"}, "color": {"purple"}, "room": {"dev"},
	})
	resp.Body.Close()

	_, page, _ := c.get("/r/general")
	if !strings.Contains(page, "さくら改") {
		t.Error("変更後の名前がページに出ていない")
	}
	if !strings.Contains(page, "pico-background-purple-550") {
		t.Error("変更後の色が反映されていない")
	}
}

func TestLeaveClearsSession(t *testing.T) {
	h := newHarness(t)
	c := h.join(t, "さくら", "cyan")

	c.form("/leave", url.Values{}).Body.Close()

	status, _, hdr := c.get("/r/general")
	if status != http.StatusSeeOther || hdr.Get("Location") != "/" {
		t.Errorf("退室後も入れてしまう: status=%d location=%q", status, hdr.Get("Location"))
	}
}
