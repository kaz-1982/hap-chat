// Package testdb は結合テスト用に、まっさらな MySQL データベースを用意する。
// docker compose の MySQL が動いていなければテストは skip される。
package testdb

import (
	"context"
	"os"
	"testing"
	"time"

	mysqldrv "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"hapchat/internal/store"
)

// DSN は docker-compose.yml の root 権限を使う。
// テスト用のデータベースを作る必要があるので、アプリ用の hap ユーザーでは足りない。
const defaultDSN = "root:root@tcp(127.0.0.1:3306)/hapchat_test?charset=utf8mb4&parseTime=True&loc=Local&multiStatements=true"

// EnvDSN を設定すると接続先を差し替えられる（CI 用）。
const EnvDSN = "HAPCHAT_TEST_DSN"

// New はパッケージごとに別のデータベースを作って接続する。
// suffix でデータベース名を分けるのは、パッケージのテストが並行して走るため。
// 中身は毎回空にするので、テスト間で状態が漏れない。
func New(t *testing.T, suffix string) *store.Store {
	t.Helper()

	dsn := defaultDSN
	if v := os.Getenv(EnvDSN); v != "" {
		dsn = v
	}
	cfg, err := mysqldrv.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("DSN を解釈できません: %v", err)
	}
	dbName := cfg.DBName + "_" + suffix
	cfg.DBName = ""
	serverDSN := cfg.FormatDSN()

	quiet := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	admin, err := gorm.Open(mysql.Open(serverDSN), quiet)
	if err == nil {
		var sqlDB, e = admin.DB()
		if e == nil {
			e = sqlDB.Ping()
		}
		err = e
	}
	if err != nil {
		t.Skipf("MySQL に接続できないので skip します（docker compose up -d で起動できます）: %v", err)
	}
	if err := admin.Exec("CREATE DATABASE IF NOT EXISTS `" + dbName + "` CHARACTER SET utf8mb4").Error; err != nil {
		t.Fatalf("テスト用データベースを作れません: %v", err)
	}
	if sqlDB, e := admin.DB(); e == nil {
		_ = sqlDB.Close()
	}

	cfg.DBName = dbName
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.FormatDSN(), 5*time.Second, false)
	if err != nil {
		t.Fatalf("テスト用データベースに接続できません: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.Migrate(ctx, nil); err != nil { // テーブルだけ先に作る
		t.Fatalf("マイグレーション: %v", err)
	}
	if err := st.TruncateAll(ctx); err != nil {
		t.Fatalf("テーブルの初期化: %v", err)
	}
	if err := st.Migrate(ctx, store.DefaultRooms()); err != nil { // ルームを入れ直す
		t.Fatalf("初期ルームの投入: %v", err)
	}
	return st
}
