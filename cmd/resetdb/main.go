// resetdb は DSN に書かれたデータベースを作り直す開発用ツール。
// E2E テストを毎回まっさらな状態から始めるために使う。
//
//	go run ./cmd/resetdb -dsn "root:root@tcp(127.0.0.1:3306)/hapchat_e2e?charset=utf8mb4"
//
// アプリ本体とは別のコマンドにしてある。うっかり本番の DB を消さないため。
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"regexp"
	"time"

	"github.com/go-sql-driver/mysql"
)

// データベース名に使える文字だけを許す（識別子はプレースホルダにできないため）。
var safeName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func main() {
	dsn := flag.String("dsn", "", "作り直したいデータベースを含む DSN（必須）")
	wait := flag.Duration("wait", 60*time.Second, "MySQL の起動を待つ時間")
	flag.Parse()

	if *dsn == "" {
		log.Fatal("-dsn は必須です")
	}
	if err := run(*dsn, *wait); err != nil {
		log.Fatal(err)
	}
}

func run(dsn string, wait time.Duration) error {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return fmt.Errorf("DSN を解釈できません: %w", err)
	}
	name := cfg.DBName
	if !safeName.MatchString(name) {
		return fmt.Errorf("データベース名に使えない文字が入っています: %q", name)
	}

	// データベース名を外して、サーバーそのものへ繋ぐ
	cfg.DBName = ""
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return err
	}
	defer db.Close()

	deadline := time.Now().Add(wait)
	for {
		if err = db.Ping(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("MySQL に接続できません: %w", err)
		}
		time.Sleep(time.Second)
	}

	if _, err := db.Exec("DROP DATABASE IF EXISTS `" + name + "`"); err != nil {
		return fmt.Errorf("DROP DATABASE: %w", err)
	}
	if _, err := db.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4"); err != nil {
		return fmt.Errorf("CREATE DATABASE: %w", err)
	}
	log.Printf("データベース %s を作り直しました", name)
	return nil
}
