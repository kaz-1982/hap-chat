package store

import "hapchat/internal/model"

// DefaultRooms は初回起動時にだけ作られるルーム。
// slug が既にあれば作られないので、DB 側での編集は上書きされない。
func DefaultRooms() []model.Room {
	return []model.Room{
		{Slug: "general", Name: "general", Icon: "💬", Topic: "なんでも雑談する部屋", Position: 1},
		{Slug: "random", Name: "random", Icon: "🎲", Topic: "脱線・ネタ・つぶやき", Position: 2},
		{Slug: "dev", Name: "dev", Icon: "🛠", Topic: "htmx / Alpine.js / Pico.css の話", Position: 3},
	}
}
