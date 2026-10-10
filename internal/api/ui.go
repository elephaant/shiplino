// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package api

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist holds the built web app (make ui). Without it, the fallback page
// (index.html in this package) is served instead.
//
//go:embed all:dist
var dist embed.FS

func webApp() (fs.FS, bool) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}

// serveUI serves the static web app: exact files, then directory
// index.html (trailing-slash export), then 404.html.
func (s *Server) serveUI(app fs.FS) http.HandlerFunc {
	files := http.FileServerFS(app)
	return func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		target := ""
		if fi, err := fs.Stat(app, p); err == nil {
			if fi.IsDir() {
				if _, err := fs.Stat(app, path.Join(p, "index.html")); err == nil {
					target = path.Join(p, "index.html")
				}
			} else {
				target = p
			}
		}
		if target == "" {
			if _, err := fs.Stat(app, p+".html"); err == nil {
				target = p + ".html"
			}
		}
		if target == "" {
			s.setCookie(w)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			if b, err := fs.ReadFile(app, "404.html"); err == nil {
				_, _ = w.Write(b)
			}
			return
		}
		if strings.HasSuffix(target, ".html") {
			s.setCookie(w) // pages hand the browser its session cookie
		} else if strings.HasPrefix(target, "_next/static/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + target
		if strings.HasSuffix(target, "index.html") {
			// FileServer redirects ".../index.html" to "./"; serve the file directly.
			b, err := fs.ReadFile(app, target)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(b)
			return
		}
		files.ServeHTTP(w, r2)
	}
}
