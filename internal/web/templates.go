package web

import (
	"html"
	"html/template"
	"io"
	"io/fs"
	"regexp"
	"strings"
	"sync"

	"hapchat/internal/chat"
	"hapchat/internal/model"
)

// templates はテンプレートの読み込みと描画。dev=true なら毎回ディスクから読み直す。
type templates struct {
	fsys fs.FS
	dev  bool
	mu   sync.RWMutex
	t    *template.Template
}

func newTemplates(fsys fs.FS, dev bool) (*templates, error) {
	tt := &templates{fsys: fsys, dev: dev}
	t, err := tt.parse()
	if err != nil {
		return nil, err
	}
	tt.t = t
	return tt, nil
}

func (tt *templates) parse() (*template.Template, error) {
	return template.New("").Funcs(funcs).ParseFS(tt.fsys,
		"templates/*.html", "templates/partials/*.html")
}

func (tt *templates) get() (*template.Template, error) {
	if !tt.dev {
		tt.mu.RLock()
		defer tt.mu.RUnlock()
		return tt.t, nil
	}
	t, err := tt.parse()
	if err != nil {
		return nil, err
	}
	tt.mu.Lock()
	tt.t = t
	tt.mu.Unlock()
	return t, nil
}

func (tt *templates) execute(w io.Writer, name string, data any) error {
	t, err := tt.get()
	if err != nil {
		return err
	}
	return t.ExecuteTemplate(w, name, data)
}

// renderEvent は SSE で流す HTML 断片を組み立てる。受信者ごとに描画するので
// 「自分の発言」の見た目をサーバー側で決められる。
func (tt *templates) renderEvent(me model.User, e chat.Event) (string, error) {
	var sb strings.Builder
	var err error
	switch e.Kind {
	case "message":
		err = tt.execute(&sb, "message", MsgView{Msg: e.Message, Me: me})
	case "presence":
		err = tt.execute(&sb, "presence", map[string]any{"Users": e.Users, "Me": me})
	case "typing":
		// 自分自身の「入力中…」は出さない
		var names []string
		for _, u := range e.Typing {
			if u.ID != me.ID {
				names = append(names, u.Name)
			}
		}
		err = tt.execute(&sb, "typing", map[string]any{"Typing": names})
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sb.String()), nil
}

// MsgView は 1 件のメッセージを「誰から見ているか」付きで渡すための入れ物。
type MsgView struct {
	Msg model.Message
	Me  model.User
}

// Mine は自分の発言かどうか。テンプレートで右寄せ表示に使う。
func (v MsgView) Mine() bool { return v.Msg.UserID != nil && *v.Msg.UserID == v.Me.ID }

var (
	reCode    = regexp.MustCompile("`[^`]+`")
	reURL     = regexp.MustCompile(`https?://[^\s<]+`)
	reMention = regexp.MustCompile(`@[0-9A-Za-z_\p{Hiragana}\p{Katakana}\p{Han}ー]{1,20}`)
)

// richText は本文を安全にエスケープしたうえで、`code` / URL / @メンション を装飾する。
// Pico の <code> と <mark> の見た目をそのまま使う。
func richText(s string) template.HTML {
	var out strings.Builder
	last := 0
	for _, loc := range reCode.FindAllStringIndex(s, -1) {
		out.WriteString(decorate(s[last:loc[0]]))
		code := html.EscapeString(s[loc[0]+1 : loc[1]-1])
		out.WriteString("<code>" + code + "</code>")
		last = loc[1]
	}
	out.WriteString(decorate(s[last:]))
	return template.HTML(strings.ReplaceAll(out.String(), "\n", "<br>"))
}

// decorate はコードスパン以外の部分を装飾する。
func decorate(s string) string {
	esc := html.EscapeString(s)
	esc = reURL.ReplaceAllStringFunc(esc, func(u string) string {
		return `<a href="` + u + `" target="_blank" rel="noopener noreferrer">` + u + `</a>`
	})
	esc = reMention.ReplaceAllStringFunc(esc, func(m string) string {
		return "<mark>" + m + "</mark>"
	})
	return esc
}

var funcs = template.FuncMap{
	"richText": richText,
	"msgview": func(m model.Message, me model.User) MsgView {
		return MsgView{Msg: m, Me: me}
	},
	"join": strings.Join,
	// dict はテンプレート内で名前付き引数を組み立てるための小道具。
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			k, _ := kv[i].(string)
			m[k] = kv[i+1]
		}
		return m
	},
}
