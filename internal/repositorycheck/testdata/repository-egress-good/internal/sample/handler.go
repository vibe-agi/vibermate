package sample

import "net/http"

func Handler() http.Handler {
	return http.NotFoundHandler()
}
