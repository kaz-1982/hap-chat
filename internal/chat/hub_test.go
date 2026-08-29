package chat

import (
	"context"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"hapchat/internal/model"
)

// fakeStore は DB の代わり。Hub のテストに MySQL を要らなくするためのもの。
type fakeStore struct {
	mu   sync.Mutex
	msgs []model.Message
	seq  uint
}

func (f *fakeStore) Rooms(context.Context) ([]model.Room, error) {
	return []model.Room{{ID: 1, Slug: "general", Name: "general"}}, nil
}

func (f *fakeStore) EnsureWelcome(ctx context.Context, roomID uint, text string) error {
	return f.Add(ctx, &model.Message{RoomID: roomID, Kind: model.KindSystem, Text: text})
}

func (f *fakeStore) Recent(_ context.Context, roomID uint, limit int) ([]model.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.Message
	for _, m := range f.msgs {
		if m.RoomID == roomID {
			out = append(out, m)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (f *fakeStore) Since(_ context.Context, roomID, afterID uint, limit int) ([]model.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.Message
	for _, m := range f.msgs {
		if m.RoomID == roomID && m.ID > afterID {
			out = append(out, m)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) Add(_ context.Context, m *model.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	m.ID = f.seq
	m.CreatedAt = time.Now()
	f.msgs = append(f.msgs, *m)
	return nil
}

// texts は保存された本文の一覧。入退室のお知らせの検証に使う。
func (f *fakeStore) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.msgs))
	for _, m := range f.msgs {
		out = append(out, m.Text)
	}
	return out
}

func newTestHub(t *testing.T) (*Hub, *fakeStore) {
	t.Helper()
	st := &fakeStore{}
	h, err := NewHub(t.Context(), st)
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	return h, st
}

var alice = model.User{ID: 1, PublicID: "a", Name: "あかり", Color: "pink"}
var bob = model.User{ID: 2, PublicID: "b", Name: "ボブ", Color: "jade"}

// drain はその時点で溜まっているイベントを取り出す。
func drain(sub *Subscriber) []Event {
	synctest.Wait()
	var out []Event
	for {
		select {
		case e, ok := <-sub.C:
			if !ok {
				return out
			}
			out = append(out, e)
		default:
			return out
		}
	}
}

func countKind(events []Event, kind string) int {
	n := 0
	for _, e := range events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func containsText(texts []string, sub string) bool {
	for _, s := range texts {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// 「入力中…」は typingTTL 経過で自動的に消える。
func TestTypingExpiresAfterTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _ := newTestHub(t)
		sub := h.Subscribe(t.Context(), 1, alice)
		drain(sub)

		h.Typing(1, bob)
		if got := h.TypingUsers(1); len(got) != 1 {
			t.Fatalf("入力中 = %d 人, want 1", len(got))
		}

		// TTL 手前ではまだ残っている
		time.Sleep(typingTTL - time.Second)
		synctest.Wait()
		if got := h.TypingUsers(1); len(got) != 1 {
			t.Fatalf("TTL 手前で消えた: %d 人", len(got))
		}

		// TTL を過ぎたら消え、消えたことが配信される
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if got := h.TypingUsers(1); len(got) != 0 {
			t.Fatalf("TTL 後も残っている: %d 人", len(got))
		}
		if countKind(drain(sub), "typing") == 0 {
			t.Error("入力中が消えたことが配信されていない")
		}
		h.Unsubscribe(sub)
	})
}

// 発言すると入力中フラグは即座に落ちる。
func TestPostClearsTyping(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _ := newTestHub(t)
		h.Typing(1, alice)
		if _, err := h.Post(t.Context(), 1, alice, "書いた"); err != nil {
			t.Fatalf("Post: %v", err)
		}
		if got := h.TypingUsers(1); len(got) != 0 {
			t.Errorf("発言後も入力中が残っている: %d 人", len(got))
		}
	})
}

// リロードやルーム移動で一瞬切れただけなら、退室も再参加も出さない。
func TestReconnectWithinGraceIsSilent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, st := newTestHub(t)

		first := h.Subscribe(t.Context(), 1, alice)
		drain(first)
		h.Unsubscribe(first)

		time.Sleep(leaveGrace - time.Second) // 猶予内に戻ってくる
		second := h.Subscribe(t.Context(), 1, alice)
		time.Sleep(leaveGrace + time.Second) // 猶予を過ぎても何も出ないこと
		synctest.Wait()

		texts := st.texts()
		if containsText(texts, "退室") {
			t.Errorf("猶予内の再接続で退室が出た: %v", texts)
		}
		joins := 0
		for _, s := range texts {
			if strings.Contains(s, "参加しました") {
				joins++
			}
		}
		if joins != 1 {
			t.Errorf("参加のお知らせが %d 回。1 回のはず: %v", joins, texts)
		}
		h.Unsubscribe(second)
	})
}

// 本当に離脱したときは、猶予のあとで退室を知らせる。
func TestLeaveAnnouncedAfterGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, st := newTestHub(t)

		sub := h.Subscribe(t.Context(), 1, alice)
		drain(sub)
		h.Unsubscribe(sub)

		// 猶予中はまだ黙っている
		time.Sleep(leaveGrace - time.Second)
		synctest.Wait()
		if containsText(st.texts(), "退室") {
			t.Fatal("猶予中に退室が出てしまった")
		}

		time.Sleep(2 * time.Second)
		synctest.Wait()
		if !containsText(st.texts(), "あかり さんが退室しました") {
			t.Errorf("猶予後も退室が出ない: %v", st.texts())
		}
	})
}

// 同じ人がタブを 2 枚開いても、参加のお知らせは 1 回だけ。
func TestJoinAnnouncedOncePerUser(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, st := newTestHub(t)

		tab1 := h.Subscribe(t.Context(), 1, alice)
		tab2 := h.Subscribe(t.Context(), 1, alice)
		synctest.Wait()

		joins := 0
		for _, s := range st.texts() {
			if strings.Contains(s, "あかり さんが参加しました") {
				joins++
			}
		}
		if joins != 1 {
			t.Errorf("参加のお知らせが %d 回。1 回のはず", joins)
		}

		// 在室リストでも 1 人として数える
		if got := h.Users(1); len(got) != 1 {
			t.Errorf("在室 = %d 人, want 1", len(got))
		}
		h.Unsubscribe(tab1)
		h.Unsubscribe(tab2)
	})
}

// 発言は保存され、同じ部屋の全員に配られる。
func TestPostPersistsAndBroadcasts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, st := newTestHub(t)
		a := h.Subscribe(t.Context(), 1, alice)
		b := h.Subscribe(t.Context(), 1, bob)
		drain(a)
		drain(b)

		msg, err := h.Post(t.Context(), 1, alice, "こんにちは")
		if err != nil {
			t.Fatalf("Post: %v", err)
		}
		if msg.ID == 0 {
			t.Error("保存後に ID が入っていない")
		}
		if msg.UserID == nil || *msg.UserID != alice.ID {
			t.Error("発言に投稿者が紐づいていない")
		}
		if !containsText(st.texts(), "こんにちは") {
			t.Error("発言が保存されていない")
		}
		for name, sub := range map[string]*Subscriber{"送信者": a, "他の人": b} {
			if countKind(drain(sub), "message") != 1 {
				t.Errorf("%s に発言が届いていない", name)
			}
		}
		h.Unsubscribe(a)
		h.Unsubscribe(b)
	})
}

// 別の部屋の発言は混ざらない。
func TestPostDoesNotLeakAcrossRooms(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _ := newTestHub(t)
		sub := h.Subscribe(t.Context(), 1, alice)
		drain(sub)

		if _, err := h.Post(t.Context(), 2, bob, "別の部屋"); err != nil {
			t.Fatalf("Post: %v", err)
		}
		if n := countKind(drain(sub), "message"); n != 0 {
			t.Errorf("別の部屋の発言が %d 件届いた", n)
		}
		h.Unsubscribe(sub)
	})
}

// 読まない接続が 1 本あっても、他の人への配信は止まらない。
func TestBroadcastSkipsStuckSubscriber(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _ := newTestHub(t)
		stuck := h.Subscribe(t.Context(), 1, alice) // 一度も読まない
		healthy := h.Subscribe(t.Context(), 1, bob)
		drain(healthy)

		// チャネルのバッファ（32）を超える回数を投げ込む
		for i := 0; i < 60; i++ {
			if _, err := h.Post(t.Context(), 1, bob, "連投"); err != nil {
				t.Fatalf("Post が詰まった: %v", err)
			}
			drain(healthy)
		}
		h.Unsubscribe(stuck)
		h.Unsubscribe(healthy)
	})
}
