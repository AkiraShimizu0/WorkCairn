package httpapi

import (
	"bytes"
	"embed"
	"net/http"
	"path"
	"time"
)

//go:embed web/*
var webUI embed.FS

func (handler *Handler) webIndex(response http.ResponseWriter, request *http.Request) {
	handler.serveWebFile(response, request, "web/index.html", "text/html; charset=utf-8")
}

func (handler *Handler) webAsset(response http.ResponseWriter, request *http.Request) {
	name := path.Base(request.PathValue("name"))
	contentType := "application/octet-stream"
	switch name {
	case "app.js":
		contentType = "text/javascript; charset=utf-8"
	case "styles.css":
		contentType = "text/css; charset=utf-8"
	default:
		http.NotFound(response, request)
		return
	}
	handler.serveWebFile(response, request, "web/"+name, contentType)
}

func (handler *Handler) webManifest(response http.ResponseWriter, request *http.Request) {
	handler.serveWebFile(response, request, "web/manifest.webmanifest", "application/manifest+json; charset=utf-8")
}

func (handler *Handler) serveWebFile(response http.ResponseWriter, request *http.Request, name, contentType string) {
	content, err := webUI.ReadFile(name)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	response.Header().Set("Content-Type", contentType)
	response.Header().Set("Cache-Control", "no-store")
	http.ServeContent(response, request, path.Base(name), time.Time{}, bytes.NewReader(content))
}
