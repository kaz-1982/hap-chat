// Package chat は「今つながっている人たち」を扱う。
// 発言そのものの保存は internal/store（MySQL + GORM）に任せ、
// ここは配信・在室・入力中といった接続に紐づく状態だけを持つ。
package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"sort"
	"sync"
	"time"

	"hapchat/internal/model"
)

// Store は Hub が必要とする永続化の操作だけを並べたもの。
// 実体は internal/store.Store だが、インターフェースにしておくことで
// Hub 単体のテストが DB なしで書ける。
type Store interface {
	Rooms(ctx context.Context) ([]model.Room, error)
	EnsureWelcome(ctx context.Context, roomID uint, text string) error
	Recent(ctx context.Context, roomID uint, limit int) ([]model.Message, error)
	Add(ctx context.Context, m *model.Message) error
}

// 画面に読み込む履歴の件数（DB にはすべて残る）
const HistoryLimit = 200

// 「入力中…」が自動的に消えるまでの時間
const typingTTL = 4 * time.Second

// リロードやルーム切り替えで一瞬切断されるだけの場合に
// 「退室しました → 参加しました」が続けて出るのを防ぐ猶予。
const leaveGrace = 3 * time.Second

// Event は SSE で各接続へ配るドメインイベント。HTML への変換は web 層が行う。
type Event struct {
	Kind    string // "message" | "presence" | "typing"
	RoomID  uint
	Message model.Message
	Users   []model.User
	Typing  []model.User
}

// Subscriber は 1 本の SSE 接続に対応する。
type Subscriber struct {
	C      chan Event
	RoomID uint
	User   model.User
}

// Hub はルーム一覧（起動時に DB から読む）と、接続まわりの状態を持つ。
type Hub struct {
	st Store

	mu      sync.RWMutex
	rooms   []model.Room
	bySlug  map[string]model.Room
	subs    map[uint]map[*Subscriber]struct{}
	typing  map[uint]map[uint]typingState
	pending map[uint]map[uint]*leaveNotice
}

type typingState struct {
	user  model.User
	until time.Time
}

// leaveNotice は「猶予つきの退室通知」。猶予内に戻ってきたら取り消される。
type leaveNotice struct {
	timer     *time.Timer
	cancelled bool
}

// NewHub はルーム一覧を DB から読み込んで Hub を作る。
// ctx が終わると入力中の掃除役（janitor）も止まる。
func NewHub(ctx context.Context, st Store) (*Hub, error) {
	rooms, err := st.Rooms(ctx)
	if err != nil {
		return nil, err
	}
	h := &Hub{
		st:      st,
		rooms:   rooms,
		bySlug:  make(map[string]model.Room, len(rooms)),
		subs:    map[uint]map[*Subscriber]struct{}{},
		typing:  map[uint]map[uint]typingState{},
		pending: map[uint]map[uint]*leaveNotice{},
	}
	for _, r := range rooms {
		h.bySlug[r.Slug] = r
		if err := st.EnsureWelcome(ctx, r.ID, "ようこそ！ここは #"+r.Name+" です。"); err != nil {
			return nil, err
		}
	}
	go h.janitor(ctx)
	return h, nil
}

// janitor は期限切れの「入力中…」を掃除して、変化があれば配信する。
func (h *Hub) janitor(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		h.mu.Lock()
		var changed []uint
		now := time.Now()
		for roomID, m := range h.typing {
			for uid, st := range m {
				if now.After(st.until) {
					delete(m, uid)
					changed = append(changed, roomID)
				}
			}
		}
		h.mu.Unlock()
		for _, roomID := range changed {
			h.broadcast(Event{Kind: "typing", RoomID: roomID, Typing: h.TypingUsers(roomID)})
		}
	}
}

func (h *Hub) Rooms() []model.Room { return h.rooms }

// Room は URL の slug からルームを引く。
func (h *Hub) Room(slug string) (model.Room, bool) {
	r, ok := h.bySlug[slug]
	return r, ok
}

// History はその部屋の直近の発言を古い順で返す。
func (h *Hub) History(ctx context.Context, roomID uint) ([]model.Message, error) {
	return h.st.Recent(ctx, roomID, HistoryLimit)
}

// Subscribe は SSE 接続を登録し、在室リストの更新を全員へ配る。
func (h *Hub) Subscribe(ctx context.Context, roomID uint, u model.User) *Subscriber {
	s := &Subscriber{C: make(chan Event, 32), RoomID: roomID, User: u}

	h.mu.Lock()
	if h.subs[roomID] == nil {
		h.subs[roomID] = map[*Subscriber]struct{}{}
	}
	first := !h.userPresentLocked(roomID, u.ID)
	// 猶予中の退室通知があれば取り消す（＝ただのリロードだった）
	if n, ok := h.pending[roomID][u.ID]; ok {
		n.cancelled = true
		n.timer.Stop()
		delete(h.pending[roomID], u.ID)
		first = false
	}
	h.subs[roomID][s] = struct{}{}
	h.mu.Unlock()

	if first {
		h.System(ctx, roomID, u.Name+" さんが参加しました")
	}
	h.broadcastPresence(roomID)
	return s
}

// Unsubscribe は接続を外す。最後の 1 本なら猶予後に退室を告知する。
func (h *Hub) Unsubscribe(s *Subscriber) {
	h.mu.Lock()
	if set, ok := h.subs[s.RoomID]; ok {
		delete(set, s)
	}
	delete(h.typing[s.RoomID], s.User.ID)
	if !h.userPresentLocked(s.RoomID, s.User.ID) {
		h.scheduleLeaveLocked(s.RoomID, s.User)
	}
	h.mu.Unlock()
	close(s.C)

	h.broadcastPresence(s.RoomID)
	h.broadcast(Event{Kind: "typing", RoomID: s.RoomID, Typing: h.TypingUsers(s.RoomID)})
}

// scheduleLeaveLocked は猶予後に退室を告知するタイマーを仕掛ける。h.mu を保持して呼ぶこと。
func (h *Hub) scheduleLeaveLocked(roomID uint, u model.User) {
	if h.pending[roomID] == nil {
		h.pending[roomID] = map[uint]*leaveNotice{}
	}
	if old, ok := h.pending[roomID][u.ID]; ok {
		old.cancelled = true
		old.timer.Stop()
	}
	n := &leaveNotice{}
	n.timer = time.AfterFunc(leaveGrace, func() {
		h.mu.Lock()
		if n.cancelled {
			h.mu.Unlock()
			return
		}
		delete(h.pending[roomID], u.ID)
		still := h.userPresentLocked(roomID, u.ID)
		h.mu.Unlock()
		if !still {
			h.System(context.Background(), roomID, u.Name+" さんが退室しました")
			h.broadcastPresence(roomID)
		}
	})
	h.pending[roomID][u.ID] = n
}

func (h *Hub) userPresentLocked(roomID, userID uint) bool {
	for s := range h.subs[roomID] {
		if s.User.ID == userID {
			return true
		}
	}
	return false
}

// Users は在室ユーザー（重複排除・名前順）を返す。接続そのものが在室の実体なので DB は見ない。
func (h *Hub) Users(roomID uint) []model.User {
	h.mu.RLock()
	defer h.mu.RUnlock()
	seen := map[uint]model.User{}
	for s := range h.subs[roomID] {
		seen[s.User.ID] = s.User
	}
	out := make([]model.User, 0, len(seen))
	for _, u := range seen {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Post は発言を保存してからルーム全員へ配信する。
func (h *Hub) Post(ctx context.Context, roomID uint, u model.User, text string) (model.Message, error) {
	m := model.Message{RoomID: roomID, UserID: &u.ID, User: &u, Kind: model.KindChat, Text: text}
	if err := h.st.Add(ctx, &m); err != nil {
		return model.Message{}, err
	}
	h.mu.Lock()
	delete(h.typing[roomID], u.ID)
	h.mu.Unlock()

	h.broadcast(Event{Kind: "message", RoomID: roomID, Message: m})
	h.broadcast(Event{Kind: "typing", RoomID: roomID, Typing: h.TypingUsers(roomID)})
	return m, nil
}

// System は入退室などのお知らせを保存して配信する。表示のためのものなので、
// 失敗しても致命的ではない（ログに残して先へ進む）。
func (h *Hub) System(ctx context.Context, roomID uint, text string) {
	m := model.Message{RoomID: roomID, Kind: model.KindSystem, Text: text}
	if err := h.st.Add(ctx, &m); err != nil {
		log.Printf("system message: %v", err)
		return
	}
	h.broadcast(Event{Kind: "message", RoomID: roomID, Message: m})
}

// Typing は「入力中…」の期限を延長して配信する。
func (h *Hub) Typing(roomID uint, u model.User) {
	h.mu.Lock()
	if h.typing[roomID] == nil {
		h.typing[roomID] = map[uint]typingState{}
	}
	h.typing[roomID][u.ID] = typingState{user: u, until: time.Now().Add(typingTTL)}
	h.mu.Unlock()
	h.broadcast(Event{Kind: "typing", RoomID: roomID, Typing: h.TypingUsers(roomID)})
}

// StopTyping は入力中フラグを即座に落とす。
func (h *Hub) StopTyping(roomID uint, u model.User) {
	h.mu.Lock()
	delete(h.typing[roomID], u.ID)
	h.mu.Unlock()
	h.broadcast(Event{Kind: "typing", RoomID: roomID, Typing: h.TypingUsers(roomID)})
}

// TypingUsers は入力中のユーザーを名前順で返す。
func (h *Hub) TypingUsers(roomID uint) []model.User {
	h.mu.RLock()
	defer h.mu.RUnlock()
	now := time.Now()
	var out []model.User
	for _, st := range h.typing[roomID] {
		if now.Before(st.until) {
			out = append(out, st.user)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (h *Hub) broadcastPresence(roomID uint) {
	h.broadcast(Event{Kind: "presence", RoomID: roomID, Users: h.Users(roomID)})
}

// broadcast は該当ルームの購読者へ配る。詰まっている接続は落とさず読み飛ばす。
func (h *Hub) broadcast(e Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.subs[e.RoomID] {
		select {
		case s.C <- e:
		default:
		}
	}
}

// NewID は Cookie に入れる公開 ID などに使う短いランダム ID。
func NewID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
