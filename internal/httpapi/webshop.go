package httpapi

import (
	"net/http"

	"lilypad/internal/deltacomm"
)

// webShopProductListRequest is POST /api/web/shop/product/list.
type webShopProductListRequest struct {
	Hdr string `json:"hdr"`
}

// handleWebShopProductList serves the WEB SHOP product list. The products are
// platform-catalog data (the sale campaign fields are not part of the game's
// master data), so they come from config. The table is server-computed: the
// response carries an empty-string hash for it and it never appears in
// confirm hashes.
func (s *Server) handleWebShopProductList(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.playerIDChecked(w, r); !ok {
		return
	}
	rows := []deltacomm.Row{}
	for _, p := range s.webShopProducts {
		row := deltacomm.Row{}
		for k, v := range p {
			row[k] = v
		}
		rows = append(rows, row)
	}
	s.respond(w, r, s.envelope(map[string]any{
		"tables": map[string]any{
			"putItems": map[string]any{"user_web_shop_product": rows},
		},
		"hashes": map[string]any{"user_web_shop_product": ""},
	}))
}
