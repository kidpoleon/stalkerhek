package webui

import (
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

var (
	bannerOnce sync.Once
	bannerPath string
)

func resolveBannerPath() string {
	bannerOnce.Do(func() {
		candidates := []string{
			filepath.Join("graphic", "banner.png"),
			filepath.Join("/app", "graphic", "banner.png"),
		}
		if root := os.Getenv("STALKERHEK_ROOT"); root != "" {
			candidates = append([]string{filepath.Join(root, "graphic", "banner.png")}, candidates...)
		}
		if exe, err := os.Executable(); err == nil {
			candidates = append(candidates, filepath.Join(filepath.Dir(exe), "graphic", "banner.png"))
		}
		for _, p := range candidates {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				bannerPath = p
				LogInfo("ASSETS", "banner: %s", p)
				return
			}
		}
		LogWarn("ASSETS", "banner.png not found; dashboard will show a broken image until graphic/banner.png is present")
	})
	return bannerPath
}

// RegisterAssetHandlers serves bundled static assets from disk (Docker: /app/graphic/).
func RegisterAssetHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/assets/banner.png", func(w http.ResponseWriter, r *http.Request) {
		p := resolveBannerPath()
		if p == "" {
			http.Error(w, "banner not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFile(w, r, p)
	})
}
