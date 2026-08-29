// Package store は MySQL への読み書きをまとめる。ORM は GORM。
package store

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"hapchat/internal/model"
)

// ErrNotFound は見つからなかったとき。呼び出し側は gorm を知らなくて済む。
var ErrNotFound = errors.New("store: not found")

type Store struct{ db *gorm.DB }

// Open は MySQL に接続する。コンテナの起動待ちがあるので wait の間リトライする。
func Open(ctx context.Context, dsn string, wait time.Duration, verbose bool) (*Store, error) {
	level := logger.Warn
	if verbose {
		level = logger.Info
	}
	cfg := &gorm.Config{
		Logger: logger.New(log.New(os.Stderr, "\n", log.LstdFlags), logger.Config{
			SlowThreshold: 200 * time.Millisecond,
			LogLevel:      level,
			// 古い Cookie が来るたびに「record not found」が出るのを止める。
			IgnoreRecordNotFoundError: true,
			Colorful:                  true,
		}),
		SkipDefaultTransaction: true,
	}

	deadline := time.Now().Add(wait)
	for attempt := 1; ; attempt++ {
		db, err := gorm.Open(mysql.Open(dsn), cfg)
		if err == nil {
			var sqlDB, e = db.DB()
			if e == nil {
				e = sqlDB.PingContext(ctx)
			}
			if e == nil {
				sqlDB.SetMaxOpenConns(25)
				sqlDB.SetMaxIdleConns(5)
				sqlDB.SetConnMaxLifetime(time.Hour)
				return &Store{db: db}, nil
			}
			err = e
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("MySQL に接続できません（docker compose up -d は済んでいますか？）: %w", err)
		}
		if attempt == 1 {
			log.Println("MySQL の起動を待っています…")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Migrate はテーブルを作り、初期ルームを流し込む。
func (s *Store) Migrate(ctx context.Context, rooms []model.Room) error {
	if err := s.db.WithContext(ctx).AutoMigrate(&model.User{}, &model.Room{}, &model.Message{}); err != nil {
		return fmt.Errorf("automigrate: %w", err)
	}
	for _, r := range rooms {
		// slug が既にあれば作らない（トピックの手編集を上書きしない）
		if err := s.db.WithContext(ctx).
			Where(model.Room{Slug: r.Slug}).
			Attrs(r).
			FirstOrCreate(&model.Room{}).Error; err != nil {
			return fmt.Errorf("seed room %s: %w", r.Slug, err)
		}
	}
	return nil
}

// TruncateAll は全テーブルを空にする。テストの前処理専用。
func (s *Store) TruncateAll(ctx context.Context) error {
	db := s.db.WithContext(ctx)
	if err := db.Exec("SET FOREIGN_KEY_CHECKS = 0").Error; err != nil {
		return err
	}
	for _, table := range []string{"messages", "users", "rooms"} {
		if err := db.Exec("TRUNCATE TABLE `" + table + "`").Error; err != nil {
			return err
		}
	}
	return db.Exec("SET FOREIGN_KEY_CHECKS = 1").Error
}

// DeleteUserForTest / DeleteRoomForTest は外部キーの挙動を確かめるためだけのもの。
// アプリ本体からは呼ばない。
func (s *Store) DeleteUserForTest(ctx context.Context, id uint) error {
	return s.db.WithContext(ctx).Delete(&model.User{}, id).Error
}

func (s *Store) DeleteRoomForTest(ctx context.Context, id uint) error {
	return s.db.WithContext(ctx).Delete(&model.Room{}, id).Error
}

// ---- ルーム ----

func (s *Store) Rooms(ctx context.Context) ([]model.Room, error) {
	var rooms []model.Room
	err := s.db.WithContext(ctx).Order("position asc, id asc").Find(&rooms).Error
	return rooms, err
}

// ---- ユーザー ----

func (s *Store) CreateUser(ctx context.Context, publicID, name, color string) (model.User, error) {
	u := model.User{PublicID: publicID, Name: name, Color: color}
	if err := s.db.WithContext(ctx).Create(&u).Error; err != nil {
		return model.User{}, err
	}
	return u, nil
}

func (s *Store) UserByPublicID(ctx context.Context, publicID string) (model.User, error) {
	var u model.User
	err := s.db.WithContext(ctx).Where("public_id = ?", publicID).First(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.User{}, ErrNotFound
	}
	return u, err
}

func (s *Store) UpdateProfile(ctx context.Context, id uint, name, color string) error {
	return s.db.WithContext(ctx).Model(&model.User{}).
		Where("id = ?", id).
		Updates(map[string]any{"name": name, "color": color}).Error
}

// ---- 発言 ----

// Recent は直近 limit 件を古い順で返す（画面にそのまま並べられる順序）。
func (s *Store) Recent(ctx context.Context, roomID uint, limit int) ([]model.Message, error) {
	var msgs []model.Message
	err := s.db.WithContext(ctx).
		// Room は呼び出し側が既に持っているので User だけを引く。
		// clause.Associations にすると rooms への SELECT が 1 本増える。
		Preload("User").
		Where("room_id = ?", roomID).
		Order("id desc").
		Limit(limit).
		Find(&msgs).Error
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// Since は afterID より後の発言を古い順で返す。
// SSE を張る前に投稿された分を拾い直すために使う。
func (s *Store) Since(ctx context.Context, roomID, afterID uint, limit int) ([]model.Message, error) {
	var msgs []model.Message
	err := s.db.WithContext(ctx).
		Preload("User").
		Where("room_id = ? AND id > ?", roomID, afterID).
		Order("id asc").
		Limit(limit).
		Find(&msgs).Error
	return msgs, err
}

// EnsureWelcome はその部屋がまだ空のときだけ、最初のお知らせを 1 件入れる。
func (s *Store) EnsureWelcome(ctx context.Context, roomID uint, text string) error {
	var n int64
	if err := s.db.WithContext(ctx).Model(&model.Message{}).
		Where("room_id = ?", roomID).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	return s.Add(ctx, &model.Message{RoomID: roomID, Kind: model.KindSystem, Text: text})
}

// Add は発言を保存する。保存後の m には ID と CreatedAt が入る。
func (s *Store) Add(ctx context.Context, m *model.Message) error {
	// Room / User は関連の書き込みをしたくないので省いて INSERT する
	if err := s.db.WithContext(ctx).Omit(clause.Associations).Create(m).Error; err != nil {
		return err
	}
	return nil
}
