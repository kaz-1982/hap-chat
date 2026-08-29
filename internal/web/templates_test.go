package web

import (
	"strings"
	"testing"

	"hapchat/internal/model"
)

// richText はユーザーが書いた文字列を HTML に埋め込む唯一の場所なので、
// エスケープが崩れていないかをまず確かめる。
func TestRichTextEscapesHTML(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		notWant string
	}{
		{
			name:    "script タグはそのまま出さない",
			in:      `<script>alert(1)</script>`,
			want:    "&lt;script&gt;",
			notWant: "<script>",
		},
		{
			name:    "属性を閉じる引用符もエスケープする",
			in:      `" onmouseover="alert(1)`,
			want:    "&#34;",
			notWant: `" onmouseover="`,
		},
		{
			name: "アンパサンドは実体参照になる",
			in:   "a & b",
			want: "a &amp; b",
		},
		{
			name:    "コードスパンの中身もエスケープする",
			in:      "`<b>bold</b>`",
			want:    "<code>&lt;b&gt;bold&lt;/b&gt;</code>",
			notWant: "<b>bold</b>",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := string(richText(tc.in))
			if !strings.Contains(got, tc.want) {
				t.Errorf("richText(%q) = %q, %q を含むはず", tc.in, got, tc.want)
			}
			if tc.notWant != "" && strings.Contains(got, tc.notWant) {
				t.Errorf("richText(%q) = %q, %q を含んではいけない", tc.in, got, tc.notWant)
			}
		})
	}
}

func TestRichTextDecorations(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"メンションは mark になる", "@さくら おはよう", "<mark>@さくら</mark>"},
		{"URL はリンクになる", "https://picocss.com を見て", `<a href="https://picocss.com"`},
		{"リンクは新しいタブで開く", "https://example.com", `rel="noopener noreferrer"`},
		{"コードスパンは code になる", "`go run .` で起動", "<code>go run .</code>"},
		{"改行は br になる", "1 行目\n2 行目", "1 行目<br>2 行目"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(richText(tc.in)); !strings.Contains(got, tc.want) {
				t.Errorf("richText(%q) = %q, %q を含むはず", tc.in, got, tc.want)
			}
		})
	}
}

// コードスパンの中の URL までリンクにしてしまうと、コード表示が壊れる。
func TestRichTextDoesNotLinkifyInsideCode(t *testing.T) {
	got := string(richText("`https://example.com`"))
	if strings.Contains(got, "<a href") {
		t.Errorf("コードスパンの中はリンクにしないはず: %q", got)
	}
}

func TestMsgViewMine(t *testing.T) {
	me := model.User{ID: 7}
	other := uint(9)

	cases := []struct {
		name string
		msg  model.Message
		want bool
	}{
		{"自分の発言", model.Message{UserID: &me.ID}, true},
		{"他人の発言", model.Message{UserID: &other}, false},
		{"システムメッセージ（user_id は NULL）", model.Message{Kind: model.KindSystem}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (MsgView{Msg: tc.msg, Me: me}).Mine(); got != tc.want {
				t.Errorf("Mine() = %v, want %v", got, tc.want)
			}
		})
	}
}

// システムメッセージは User が nil なので、テンプレートから触ると落ちる。
// Author() がその穴を塞いでいることを確かめる。
func TestAuthorHandlesNilUser(t *testing.T) {
	m := model.Message{Kind: model.KindSystem}
	if got := m.Author().Name; got == "" {
		t.Error("Author().Name が空。テンプレートで nil 参照になる")
	}
	if got := m.Author().Initial(); got == "" {
		t.Error("Author().Initial() が空")
	}
}
