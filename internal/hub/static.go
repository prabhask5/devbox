package hub

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"mime"
	"net/http"
	"path"
	"strings"
)

// webapp is the agents app, built from web/hub-app by scripts/gen.sh.
//
//go:embed webapp
var webapp embed.FS

// staticFile is one file of the app, ready to serve.
type staticFile struct {
	data        []byte
	contentType string
	etag        string
}

var static = func() map[string]staticFile {
	files := map[string]staticFile{}
	entries, err := webapp.ReadDir("webapp")
	if err != nil {
		panic(err) // the directory is part of the binary
	}
	for _, entry := range entries {
		data, err := webapp.ReadFile("webapp/" + entry.Name())
		if err != nil {
			panic(err)
		}
		contentType := mime.TypeByExtension(path.Ext(entry.Name()))
		switch path.Ext(entry.Name()) {
		case ".webmanifest":
			contentType = "application/manifest+json"
		case ".js":
			contentType = "text/javascript; charset=utf-8"
		}
		sum := sha256.Sum256(data)
		file := staticFile{data: data, contentType: contentType, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
		files["/"+entry.Name()] = file
		if entry.Name() == "index.html" {
			files["/"] = file
		}
	}
	return files
}()

// serveStatic answers with a file of the app. The files keep their names from one version to
// the next, so browsers always ask whether theirs is still current; the service worker's cache
// is what makes the app start without the network.
func serveStatic(w http.ResponseWriter, r *http.Request) {
	file, found := static[r.URL.Path]
	if !found || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	header := w.Header()
	header.Set("Content-Type", file.contentType)
	header.Set("Cache-Control", "no-cache")
	header.Set("ETag", file.etag)
	header.Set("X-Content-Type-Options", "nosniff")
	if strings.Contains(r.Header.Get("If-None-Match"), file.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method == http.MethodGet {
		_, _ = w.Write(file.data)
	}
}
