package main

import "os"

func main() {
	if _, err := os.Stdout.WriteString("Content-Type: text/plain; charset=utf-8\n\nadmin\n"); err != nil {
		os.Exit(1)
	}
}
