package httpapi

import "net/http"

func (s *Server) masterMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/lottery/draw", "/api/item/lottery/draw", "/api/lottery/card/exchange", "/api/mission/receive", "/api/mission/loop/receive", "/api/gift/receive", "/api/daily/update", "/api/stockable_regular_reward/receive":
			if s.master == nil {
				s.fail(w, r, 1, "master data unavailable; no changes applied")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
