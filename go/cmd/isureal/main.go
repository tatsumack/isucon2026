package main

import (
	"net/http"
	_ "net/http/pprof"

	"example.com/isureal-company/isureal"
)

// 2026-09-23

func main() {
	// 計測用 (bench-capture.shから取得する)
	go func() {
		http.ListenAndServe("localhost:6060", nil)
	}()

	config := isureal.ConfigFromEnv()
	server := isureal.NewServer(config)
	if err := server.Start(":8080"); err != nil {
		panic("failed to start server: " + err.Error())
	}
}
