package main

import (
	"fmt"
	"go.mills.io/bitcask/v2"
	"log/slog"
	"os"
)

func main() {
	dir, err := os.Getwd()
	if err != nil {
		slog.Error("获取当前运行目录失败:", err)
		return
	}
	db, _ := bitcask.Open(fmt.Sprintf("%s/bitcask/data/character.db", dir))
	defer func() {
		err = db.Close()
		if err != nil {
			panic(err)
		}
	}()
	err = db.Put([]byte("Hello"), []byte("World"))
	if err != nil {
		panic(err)
	}
	val, err := db.Get([]byte("Hello"))
	if err != nil {
		panic(err)
	}
	slog.Info("val", slog.Any("val", string(val)))
}
