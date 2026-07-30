package sample

import "net/http"

func Bypass() {
	_, _ = http.Get("https://example.invalid")
}
