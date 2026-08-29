// Package model はデータベースに保存する型を定義する。
// GORM のタグを持つが、テンプレートからもそのまま使う（学習用なので層を分けすぎない）。
package model

import "time"

// User は参加者。PublicID は Cookie に入れる外向きの ID で、
// 連番の主キーをそのまま外へ出さないために持たせている。
type User struct {
	ID        uint   `gorm:"primaryKey"`
	PublicID  string `gorm:"size:32;uniqueIndex;not null"`
	Name      string `gorm:"size:40;not null"`
	Color     string `gorm:"size:20;not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Initial はアバターに出す 1 文字。テンプレートから呼ぶ。
func (u User) Initial() string {
	r := []rune(u.Name)
	if len(r) == 0 {
		return "?"
	}
	return string(r[:1])
}

// Room はチャットルーム。Slug が URL に出る（/r/general など）。
type Room struct {
	ID        uint   `gorm:"primaryKey"`
	Slug      string `gorm:"size:40;uniqueIndex;not null"`
	Name      string `gorm:"size:40;not null"`
	Icon      string `gorm:"size:16;not null"`
	Topic     string `gorm:"size:200;not null"`
	Position  int    `gorm:"not null;default:0"`
	CreatedAt time.Time
}

// Message の種別。
const (
	KindChat   = "chat"
	KindSystem = "system"
)

// Message は 1 発言。入退室のお知らせ（KindSystem）は UserID が NULL になる。
type Message struct {
	ID        uint      `gorm:"primaryKey"`
	RoomID    uint      `gorm:"not null;index:idx_room_created,priority:1"`
	Room      *Room     `gorm:"constraint:OnDelete:CASCADE"`
	UserID    *uint     `gorm:"index"`
	User      *User     `gorm:"constraint:OnDelete:SET NULL"`
	Kind      string    `gorm:"size:10;not null;default:chat"`
	Text      string    `gorm:"type:text;not null"`
	CreatedAt time.Time `gorm:"index:idx_room_created,priority:2"`
}

// IsSystem はテンプレートの分岐用。
func (m Message) IsSystem() bool { return m.Kind == KindSystem }

// Author は表示用の投稿者。システムメッセージでも nil にならないようにする。
func (m Message) Author() User {
	if m.User == nil {
		return User{Name: "system", Color: "slate"}
	}
	return *m.User
}
