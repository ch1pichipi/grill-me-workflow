// 与根目录 main.go 等价的入口，便于 `go build ./cmd/server`。
package main

import (
	"log"

	"chatmod/internal/app"
)

func main() {
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
