BIN := bin/shiplino

.PHONY: build ui ui-dev test lint clean

UI_OUT := internal/api/dist

# build compiles the binary, with the web app if Node is available.
build:
	@if command -v npm >/dev/null 2>&1; then $(MAKE) ui; else echo "npm not found: building without the web app (fallback page)"; fi
	go build -o $(BIN) ./cmd/shiplino

# ui builds the static web app and copies it where go:embed picks it up.
ui:
	cd web && npm ci --no-audit --no-fund && npm run build
	find $(UI_OUT) -mindepth 1 ! -name README.md -exec rm -rf {} +
	cp -R web/apps/local/out/. $(UI_OUT)/

# ui-dev runs the web app with hot reload against a local daemon.
ui-dev:
	@echo "Start the daemon with: SHIPLINO_DEV_ORIGIN=http://localhost:3000 shiplino daemon"
	@echo "Then visit http://localhost:4777 once (sets the session cookie) and use http://localhost:3000"
	cd web && NEXT_PUBLIC_SHIPLINO_API=http://localhost:4777 npm run dev

test:
	go test ./...

lint:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "run gofmt -w ." && exit 1)
	go vet ./...

clean:
	rm -rf bin dist web/apps/local/out web/apps/local/.next
	find $(UI_OUT) -mindepth 1 ! -name README.md -exec rm -rf {} +
