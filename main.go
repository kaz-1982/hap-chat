// HAP Chat — htmx + Alpine.js + Pico.css のショーケース。
// バックエンドは Go 標準ライブラリ + GORM（MySQL）。
package main

import (
	"context"
	"crypto/rand"
	"embed"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"hapchat/internal/chat"
	"hapchat/internal/store"
	"hapchat/internal/web"
)

//go:embed templates static
var embedded embed.FS

// defaultDSN は docker-compose.yml の設定に合わせてある。
const defaultDSN = "hap:hap@tcp(127.0.0.1:3306)/hapchat?charset=utf8mb4&parseTime=True&loc=Local"

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dsn := flag.String("dsn", envOr("HAPCHAT_DSN", defaultDSN), "MySQL の DSN")
	secret := flag.String("secret", os.Getenv("HAPCHAT_SECRET"), "セッション Cookie の署名鍵（空なら起動ごとにランダム）")
	wait := flag.Duration("db-wait", 30*time.Second, "MySQL の起動を待つ時間")
	dev := flag.Bool("dev", false, "テンプレートと静的ファイルをディスクから読む（編集して再読込するだけで反映）")
	sqlLog := flag.Bool("sql", false, "実行された SQL をログに出す")
	flag.Parse()

	var assets, tmplFS fs.FS = embedded, embedded
	if *dev {
		disk := os.DirFS(".")
		assets, tmplFS = disk, disk
		log.Println("dev モード: templates/ と static/ をディスクから読みます")
	}
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		log.Fatal(err)
	}

	// ctx をキャンセルすると Hub の掃除役も止まる。
	ctx, stopHub := context.WithCancel(context.Background())
	defer stopHub()

	st, err := store.Open(ctx, *dsn, *wait, *sqlLog)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx, store.DefaultRooms()); err != nil {
		log.Fatal(err)
	}
	log.Println("MySQL に接続しました（マイグレーション完了）")

	hub, err := chat.NewHub(ctx, st)
	if err != nil {
		log.Fatal(err)
	}

	srv, err := web.New(hub, st, sessionKey(*secret), sub, tmplFS, *dev)
	if err != nil {
		log.Fatal(err)
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           logging(srv),
		ReadHeaderTimeout: 10 * time.Second,
		// SSE を切らないため WriteTimeout は設定しない。
	}

	go func() {
		log.Printf("HAP Chat → http://localhost%s", *addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Println("shutting down…")
	stopHub()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}

// sessionKey は署名鍵。指定がなければ毎回ランダムにする
// （＝サーバーを再起動すると全員ログアウトになる。発言は DB に残る）。
func sessionKey(s string) []byte {
	if s != "" {
		return []byte(s)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Fatal(err)
	}
	log.Println("HAPCHAT_SECRET が未設定です。今回だけ有効な鍵を生成しました（再起動で再入室が必要になります）")
	return b
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// logging はリクエストを 1 行で記録する。
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
