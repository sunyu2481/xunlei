package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
)

func main() {
	var count atomic.Int64
	conn, err := net.ListenPacket("udp", ":9877")
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	go func() {
		buffer := make([]byte, 65535)
		for {
			n, _, err := conn.ReadFrom(buffer)
			if err != nil {
				return
			}
			count.Add(int64(n))
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, n)
	})
	mux.HandleFunc("/reset", func(w http.ResponseWriter, r *http.Request) { count.Store(0) })
	mux.HandleFunc("/count", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, count.Load()) })
	if err := http.ListenAndServe(":9876", mux); err != nil {
		panic(err)
	}
}
