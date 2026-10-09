The web app build (web/apps/local, `next build`) is copied here by `make ui`
and embedded into the binary. This placeholder keeps `go build` working
without Node; the daemon then serves its built-in fallback page.
