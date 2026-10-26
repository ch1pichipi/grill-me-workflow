// 命令入口。`go run .` 起一个进程，浏览器打开就是全部界面。
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
