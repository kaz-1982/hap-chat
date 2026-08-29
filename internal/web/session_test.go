package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"hapchat/internal/model"
)

func testServer() *Server { return &Server{secret: []byte("test-secret")} }

func userWithPublicID(id string) model.User {
	return model.User{PublicID: id, Name: "テスト", Color: "azure"}
}

// Cookie は public_id + 署名。署名が合わない値は必ず弾かれること。
func TestVerifyCookie(t *testing.T) {
	s := testServer()
	const id = "abc123"
	good := id + "." + s.sign(id)

	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"正しい署名", good, true},
		{"署名を書き換えた", id + ".AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", false},
		{"署名なし", id, false},
		{"署名が空", id + ".", false},
		{"public_id が空", "." + s.sign(""), false},
		{"別人の public_id に差し替え", "other." + s.sign(id), false},
		{"空文字", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := s.verifyCookie(tc.value)
			if ok != tc.want {
				t.Fatalf("verifyCookie(%q) ok = %v, want %v", tc.value, ok, tc.want)
			}
			if ok && got != id {
				t.Errorf("public_id = %q, want %q", got, id)
			}
		})
	}
}

// 鍵が違えば同じ public_id でも署名は通らない
// （HAPCHAT_SECRET を変えると再入室が必要になる、という挙動の裏付け）。
func TestVerifyCookieRejectsOtherKey(t *testing.T) {
	a := &Server{secret: []byte("key-a")}
	b := &Server{secret: []byte("key-b")}
	const id = "abc123"
	if _, ok := b.verifyCookie(id + "." + a.sign(id)); ok {
		t.Error("別の鍵で署名した Cookie が通ってしまった")
	}
}

func TestSetSessionCookieAttributes(t *testing.T) {
	s := testServer()
	w := httptest.NewRecorder()
	s.setSession(w, userWithPublicID("pid"))

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("Cookie が %d 個。1 個のはず", len(cookies))
	}
	c := cookies[0]
	if !c.HttpOnly {
		t.Error("HttpOnly が付いていない（JS から読めてしまう）")
	}
	if c.Path != "/" {
		t.Errorf("Path = %q, want /", c.Path)
	}
	if !strings.HasPrefix(c.Value, "pid.") {
		t.Errorf("値が public_id で始まっていない: %q", c.Value)
	}
	if _, ok := s.verifyCookie(c.Value); !ok {
		t.Error("自分で発行した Cookie を自分で検証できない")
	}
}

func TestTruncateKeepsRunesIntact(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"あいうえお", 3, "あいう"},
		{"あいう", 10, "あいう"},
		{"", 5, ""},
		{"🎉🎉🎉", 2, "🎉🎉"}, // サロゲートペアを割らない
	}
	for _, tc := range cases {
		if got := truncate(tc.in, tc.n); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestFormNameAndColor(t *testing.T) {
	t.Run("名前が空なら既定値", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/join", strings.NewReader("name=%20%20"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if got := formName(r); got != "ななし" {
			t.Errorf("formName = %q, want ななし", got)
		}
	})

	t.Run("長い名前は 20 文字に切る", func(t *testing.T) {
		long := strings.Repeat("あ", 40)
		r := httptest.NewRequest("POST", "/join", strings.NewReader("name="+long))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if got := []rune(formName(r)); len(got) != 20 {
			t.Errorf("長さ = %d, want 20", len(got))
		}
	})

	t.Run("パレットに無い色は既定値に落とす", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/join", strings.NewReader("color=url(javascript:alert(1))"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if got := formColor(r); got != Palette[0] {
			t.Errorf("formColor = %q, want %q", got, Palette[0])
		}
	})

	t.Run("パレットにある色はそのまま", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/join", strings.NewReader("color=jade"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if got := formColor(r); got != "jade" {
			t.Errorf("formColor = %q, want jade", got)
		}
	})
}
