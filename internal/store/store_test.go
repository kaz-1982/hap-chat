package store_test

import (
	"context"
	"errors"
	"testing"

	"hapchat/internal/model"
	"hapchat/internal/store"
	"hapchat/internal/testdb"
)

func setup(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	return testdb.New(t, "store"), context.Background()
}

// 初期ルームは position の順に並ぶ。
func TestRoomsSeededInOrder(t *testing.T) {
	st, ctx := setup(t)
	rooms, err := st.Rooms(ctx)
	if err != nil {
		t.Fatalf("Rooms: %v", err)
	}
	want := []string{"general", "random", "dev"}
	if len(rooms) != len(want) {
		t.Fatalf("ルーム数 = %d, want %d", len(rooms), len(want))
	}
	for i, slug := range want {
		if rooms[i].Slug != slug {
			t.Errorf("rooms[%d].Slug = %q, want %q", i, rooms[i].Slug, slug)
		}
	}
}

// 起動のたびに歓迎メッセージが積み上がらないこと。
func TestEnsureWelcomeIsIdempotent(t *testing.T) {
	st, ctx := setup(t)
	rooms, _ := st.Rooms(ctx)
	id := rooms[0].ID

	for i := 0; i < 3; i++ {
		if err := st.EnsureWelcome(ctx, id, "ようこそ"); err != nil {
			t.Fatalf("EnsureWelcome: %v", err)
		}
	}
	msgs, err := st.Recent(ctx, id, 100)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("歓迎メッセージが %d 件。1 件のはず", len(msgs))
	}
}

func TestUserRoundTrip(t *testing.T) {
	st, ctx := setup(t)

	u, err := st.CreateUser(ctx, "pid-1", "さくら", "cyan")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.ID == 0 {
		t.Error("ID が採番されていない")
	}

	got, err := st.UserByPublicID(ctx, "pid-1")
	if err != nil {
		t.Fatalf("UserByPublicID: %v", err)
	}
	if got.Name != "さくら" || got.Color != "cyan" {
		t.Errorf("読み戻した値が違う: %+v", got)
	}

	if err := st.UpdateProfile(ctx, u.ID, "さくら改", "purple"); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	got, _ = st.UserByPublicID(ctx, "pid-1")
	if got.Name != "さくら改" || got.Color != "purple" {
		t.Errorf("更新が反映されていない: %+v", got)
	}
}

// 存在しないユーザーは ErrNotFound。呼び出し側が gorm を知らずに済むこと。
func TestUserByPublicIDNotFound(t *testing.T) {
	st, ctx := setup(t)
	_, err := st.UserByPublicID(ctx, "存在しない")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want store.ErrNotFound", err)
	}
}

// Recent は古い順に返す（画面にそのまま並べられる順序）。
func TestRecentReturnsOldestFirst(t *testing.T) {
	st, ctx := setup(t)
	rooms, _ := st.Rooms(ctx)
	room := rooms[0].ID
	u, _ := st.CreateUser(ctx, "pid-1", "さくら", "cyan")

	for _, body := range []string{"1 番目", "2 番目", "3 番目"} {
		if err := st.Add(ctx, &model.Message{
			RoomID: room, UserID: &u.ID, Kind: model.KindChat, Text: body,
		}); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	msgs, err := st.Recent(ctx, room, 100)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	got := []string{}
	for _, m := range msgs {
		got = append(got, m.Text)
	}
	if len(got) != 3 || got[0] != "1 番目" || got[2] != "3 番目" {
		t.Errorf("並び順が違う: %v", got)
	}
}

// limit は「新しいほうから N 件」。古い側が落ちる。
func TestRecentLimitKeepsNewest(t *testing.T) {
	st, ctx := setup(t)
	rooms, _ := st.Rooms(ctx)
	room := rooms[0].ID
	u, _ := st.CreateUser(ctx, "pid-1", "さくら", "cyan")

	for i := 0; i < 10; i++ {
		_ = st.Add(ctx, &model.Message{RoomID: room, UserID: &u.ID, Kind: model.KindChat, Text: string(rune('a' + i))})
	}
	msgs, _ := st.Recent(ctx, room, 3)
	if len(msgs) != 3 {
		t.Fatalf("件数 = %d, want 3", len(msgs))
	}
	if msgs[0].Text != "h" {
		t.Errorf("先頭が %q。新しいほうから 3 件なので h のはず", msgs[0].Text)
	}
	if msgs[2].Text != "j" {
		t.Errorf("最後が %q。最新の j のはず", msgs[2].Text)
	}
}

// Preload("User") で投稿者が引けていること（N+1 回避の実装が効いているか）。
func TestRecentPreloadsAuthor(t *testing.T) {
	st, ctx := setup(t)
	rooms, _ := st.Rooms(ctx)
	room := rooms[0].ID
	u, _ := st.CreateUser(ctx, "pid-1", "さくら", "cyan")
	_ = st.Add(ctx, &model.Message{RoomID: room, UserID: &u.ID, Kind: model.KindChat, Text: "やあ"})

	msgs, _ := st.Recent(ctx, room, 100)
	last := msgs[len(msgs)-1]
	if last.User == nil {
		t.Fatal("User が読み込まれていない")
	}
	if last.User.Name != "さくら" {
		t.Errorf("投稿者 = %q, want さくら", last.User.Name)
	}
	if last.Author().Color != "cyan" {
		t.Errorf("色 = %q, want cyan", last.Author().Color)
	}
}

// 入退室のお知らせは user_id が NULL のまま往復できること。
func TestSystemMessageHasNoAuthor(t *testing.T) {
	st, ctx := setup(t)
	rooms, _ := st.Rooms(ctx)
	room := rooms[0].ID
	_ = st.Add(ctx, &model.Message{RoomID: room, Kind: model.KindSystem, Text: "さくら さんが参加しました"})

	msgs, _ := st.Recent(ctx, room, 100)
	last := msgs[len(msgs)-1]
	if last.UserID != nil {
		t.Errorf("user_id = %v, NULL のはず", *last.UserID)
	}
	if !last.IsSystem() {
		t.Error("IsSystem() が false")
	}
	if last.Author().Name == "" {
		t.Error("Author() が空。テンプレートで nil 参照になる")
	}
}

// utf8mb4 でないと 4 バイト文字が落ちる。
func TestEmojiRoundTrip(t *testing.T) {
	st, ctx := setup(t)
	rooms, _ := st.Rooms(ctx)
	room := rooms[0].ID
	u, _ := st.CreateUser(ctx, "pid-1", "さくら", "cyan")

	const body = "やった🎉🚀 日本語も ok"
	_ = st.Add(ctx, &model.Message{RoomID: room, UserID: &u.ID, Kind: model.KindChat, Text: body})

	msgs, _ := st.Recent(ctx, room, 100)
	if got := msgs[len(msgs)-1].Text; got != body {
		t.Errorf("読み戻し = %q, want %q", got, body)
	}
}

// ユーザーを消しても発言は残る（ON DELETE SET NULL）。
func TestDeletingUserKeepsMessages(t *testing.T) {
	st, ctx := setup(t)
	rooms, _ := st.Rooms(ctx)
	room := rooms[0].ID
	u, _ := st.CreateUser(ctx, "pid-1", "さくら", "cyan")
	_ = st.Add(ctx, &model.Message{RoomID: room, UserID: &u.ID, Kind: model.KindChat, Text: "残るはず"})

	if err := st.DeleteUserForTest(ctx, u.ID); err != nil {
		t.Fatalf("ユーザー削除: %v", err)
	}

	msgs, err := st.Recent(ctx, room, 100)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	var found *model.Message
	for i := range msgs {
		if msgs[i].Text == "残るはず" {
			found = &msgs[i]
		}
	}
	if found == nil {
		t.Fatal("ユーザーを消したら発言まで消えた")
	}
	if found.UserID != nil {
		t.Errorf("user_id = %v, NULL になるはず", *found.UserID)
	}
}

// 部屋を消すと発言も消える（ON DELETE CASCADE）。
func TestDeletingRoomRemovesMessages(t *testing.T) {
	st, ctx := setup(t)
	rooms, _ := st.Rooms(ctx)
	room := rooms[0].ID
	u, _ := st.CreateUser(ctx, "pid-1", "さくら", "cyan")
	_ = st.Add(ctx, &model.Message{RoomID: room, UserID: &u.ID, Kind: model.KindChat, Text: "消えるはず"})

	if err := st.DeleteRoomForTest(ctx, room); err != nil {
		t.Fatalf("ルーム削除: %v", err)
	}
	msgs, _ := st.Recent(ctx, room, 100)
	if len(msgs) != 0 {
		t.Errorf("部屋を消したのに発言が %d 件残っている", len(msgs))
	}
}
