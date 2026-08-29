package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"hapchat/internal/chat"
	"hapchat/internal/model"
	"hapchat/internal/store"
)

const sessionCookie = "hap_session"
const sessionTTL = 30 * 24 * time.Hour

// Palette は参加時に選べるアバター色（Pico のカラーユーティリティ名）。
var Palette = []string{"azure", "jade", "pumpkin", "purple", "pink", "cyan", "indigo", "orange"}

// Server は HTTP ハンドラ一式。
type Server struct {
	hub    *chat.Hub
	st     *store.Store
	secret []byte
	assets fs.FS
	tmpl   *templates
	mux    *http.ServeMux
}

func New(hub *chat.Hub, st *store.Store, secret []byte, assets fs.FS, tmplFS fs.FS, dev bool) (*Server, error) {
	t, err := newTemplates(tmplFS, dev)
	if err != nil {
		return nil, err
	}
	s := &Server{hub: hub, st: st, secret: secret, assets: assets, tmpl: t, mux: http.NewServeMux()}
	s.routes()
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) routes() {
	static := http.FileServer(http.FS(s.assets))
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", cacheControl(static)))

	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	s.mux.HandleFunc("POST /join", s.handleJoin)
	s.mux.HandleFunc("POST /leave", s.handleLeave)
	s.mux.HandleFunc("POST /profile", s.requireUser(s.handleProfile))

	s.mux.HandleFunc("GET /r/{room}", s.requireUser(s.handleRoom))
	s.mux.HandleFunc("GET /r/{room}/panel", s.requireUser(s.handlePanel))
	s.mux.HandleFunc("GET /r/{room}/sse", s.requireUser(s.handleSSE))
	s.mux.HandleFunc("POST /r/{room}/messages", s.requireUser(s.handlePost))
	s.mux.HandleFunc("POST /r/{room}/typing", s.requireUser(s.handleTyping))
}

func cacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "vendor/") {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		} else {
			// 自前の CSS/JS は常に検証させる（-dev で編集したらすぐ反映される）
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

// ---- セッション ----
// Cookie には users.public_id と、その HMAC 署名だけを入れる。
// 名前や色はすべて DB が持つので、Cookie を書き換えても他人にはなりすませない。

func (s *Server) sign(publicID string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(publicID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) setSession(w http.ResponseWriter, u model.User) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    u.PublicID + "." + s.sign(u.PublicID),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
}

// verifyCookie は Cookie の値から public_id を取り出す。署名が合わなければ弾く。
// DB を見ないので、ここだけ切り出して単体テストできる。
func (s *Server) verifyCookie(value string) (string, bool) {
	publicID, sig, ok := strings.Cut(value, ".")
	if !ok || publicID == "" {
		return "", false
	}
	if !hmac.Equal([]byte(sig), []byte(s.sign(publicID))) {
		return "", false
	}
	return publicID, true
}

// currentUser は Cookie の署名を確かめてから DB のユーザーを引く。
func (s *Server) currentUser(r *http.Request) (model.User, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return model.User{}, false
	}
	publicID, ok := s.verifyCookie(c.Value)
	if !ok {
		return model.User{}, false
	}
	u, err := s.st.UserByPublicID(r.Context(), publicID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Printf("load user: %v", err)
		}
		return model.User{}, false
	}
	return u, true
}

// requireUser は未参加なら入室画面へ戻す。
func (s *Server) requireUser(next func(http.ResponseWriter, *http.Request, model.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.currentUser(r)
		if !ok {
			clearSession(w)
			hxRedirect(w, r, "/")
			return
		}
		next(w, r, u)
	}
}

// ---- ページ ----

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.currentUser(r); ok {
		http.Redirect(w, r, "/r/"+s.hub.Rooms()[0].Slug, http.StatusSeeOther)
		return
	}
	s.render(w, "login", map[string]any{
		"Palette": Palette,
		"Rooms":   s.hub.Rooms(),
	})
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	name, color := formName(r), formColor(r)
	u, err := s.st.CreateUser(r.Context(), chat.NewID(), name, color)
	if err != nil {
		s.fail(w, "ユーザーを作成できませんでした", err)
		return
	}
	s.setSession(w, u)
	hxRedirect(w, r, "/r/"+s.hub.Rooms()[0].Slug)
}

func (s *Server) handleLeave(w http.ResponseWriter, r *http.Request) {
	clearSession(w)
	hxRedirect(w, r, "/")
}

// handleProfile は名前と色の変更。SSE が張り直されるので在室表示も更新される。
func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request, u model.User) {
	if err := s.st.UpdateProfile(r.Context(), u.ID, formName(r), formColor(r)); err != nil {
		s.fail(w, "プロフィールを保存できませんでした", err)
		return
	}
	slug := r.FormValue("room")
	if _, ok := s.hub.Room(slug); !ok {
		slug = s.hub.Rooms()[0].Slug
	}
	hxRedirect(w, r, "/r/"+slug)
}

func (s *Server) handleRoom(w http.ResponseWriter, r *http.Request, u model.User) {
	room, ok := s.hub.Room(r.PathValue("room"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := s.chatData(r, room, u)
	if err != nil {
		s.fail(w, "発言を読み込めませんでした", err)
		return
	}
	s.render(w, "chat", data)
}

// handlePanel はルーム切り替え用のフラグメント。中の sse-connect が張り直される。
func (s *Server) handlePanel(w http.ResponseWriter, r *http.Request, u model.User) {
	room, ok := s.hub.Room(r.PathValue("room"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := s.chatData(r, room, u)
	if err != nil {
		s.fail(w, "発言を読み込めませんでした", err)
		return
	}
	w.Header().Set("HX-Push-Url", "/r/"+room.Slug)
	s.render(w, "room-shell", data)
}

func (s *Server) chatData(r *http.Request, room model.Room, u model.User) (map[string]any, error) {
	msgs, err := s.hub.History(r.Context(), room.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"Me":       u,
		"Room":     room,
		"Rooms":    s.hub.Rooms(),
		"Messages": msgs,
		"Users":    s.hub.Users(room.ID),
		"Palette":  Palette,
	}, nil
}

// ---- 書き込み ----

func (s *Server) handlePost(w http.ResponseWriter, r *http.Request, u model.User) {
	room, ok := s.hub.Room(r.PathValue("room"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	text = truncate(text, 1000)
	if _, err := s.hub.Post(r.Context(), room.ID, u, text); err != nil {
		s.fail(w, "発言を保存できませんでした", err)
		return
	}
	// 本文は SSE 経由で届くのでレスポンスは空。
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTyping(w http.ResponseWriter, r *http.Request, u model.User) {
	room, ok := s.hub.Room(r.PathValue("room"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.FormValue("stop") == "1" {
		s.hub.StopTyping(room.ID, u)
	} else {
		s.hub.Typing(room.ID, u)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- SSE ----

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request, u model.User) {
	room, ok := s.hub.Room(r.PathValue("room"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // リバースプロキシのバッファリング抑止
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	rc.Flush()

	sub := s.hub.Subscribe(r.Context(), room.ID, u)
	defer s.hub.Unsubscribe(sub)

	// 接続直後の初期状態を送る
	s.sendEvent(w, rc, u, chat.Event{Kind: "presence", RoomID: room.ID, Users: s.hub.Users(room.ID)})
	s.sendEvent(w, rc, u, chat.Event{Kind: "typing", RoomID: room.ID, Typing: s.hub.TypingUsers(room.ID)})

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			rc.Flush()
		case e, ok := <-sub.C:
			if !ok {
				return
			}
			if err := s.sendEvent(w, rc, u, e); err != nil {
				return
			}
		}
	}
}

// sendEvent は受信者ごとに HTML を組み立てて 1 イベント分書き出す。
func (s *Server) sendEvent(w http.ResponseWriter, rc *http.ResponseController, me model.User, e chat.Event) error {
	html, err := s.tmpl.renderEvent(me, e)
	if err != nil {
		log.Printf("render event %s: %v", e.Kind, err)
		return nil
	}
	var b strings.Builder
	b.WriteString("event: " + e.Kind + "\n")
	for _, line := range strings.Split(strings.TrimRight(html, "\n"), "\n") {
		b.WriteString("data: " + line + "\n")
	}
	b.WriteString("\n")
	if _, err := w.Write([]byte(b.String())); err != nil {
		return err
	}
	return rc.Flush()
}

// ---- ヘルパ ----

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.execute(w, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

func (s *Server) fail(w http.ResponseWriter, msg string, err error) {
	log.Printf("%s: %v", msg, err)
	http.Error(w, msg, http.StatusInternalServerError)
}

func hxRedirect(w http.ResponseWriter, r *http.Request, to string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func formName(r *http.Request) string {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = "ななし"
	}
	return truncate(name, 20)
}

func formColor(r *http.Request) string {
	c := r.FormValue("color")
	for _, p := range Palette {
		if p == c {
			return c
		}
	}
	return Palette[0]
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
