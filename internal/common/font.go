package common

import (
	_ "embed"
	"net/http"
)

// Inter (SIL Open Font License 1.1, fonts/Inter-LICENSE.txt), cut down to
// Latin and Cyrillic. Served by both panels so they look the same offline:
// nothing is loaded from the internet.
//
//go:embed fonts/Inter.woff2
var interWOFF2 []byte

// FontHandler serves the panel font.
func FontHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "font/woff2")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	w.Write(interWOFF2)
}

//go:embed ui-i18n.js
var uiI18nJS []byte

// UIi18nHandler serves the panels' English translation (ui-i18n.js).
func UIi18nHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(uiI18nJS)
}
