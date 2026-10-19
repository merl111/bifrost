.PHONY: build test vet integration release desktop desktop-test desktop-smoke

build:
	go build -o bin/bifrost ./cmd/bifrost

test:
	go test -race -timeout=90s ./...

vet:
	go vet ./...

integration:
	BIFROST_BROWSER_TEST=1 BIFROST_TERMINAL_TEST=1 BIFROST_QEMU_INTEGRATION=1 go test -race -count=1 -timeout=90s ./...

release:
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/bifrost ./cmd/bifrost

# Optional native GUI. Its module is deliberately outside the default Go build.
# Keep a CUI checkout alongside Bifrost; see docs/desktop.md for custom paths.
desktop:
	cmake -S ../cui -B ../cui/build -DCMAKE_BUILD_TYPE=Release
	cmake --build ../cui/build --target cui --parallel 4
	cd desktop && go build -ldflags="-buildid=bifrost-desktop-$$(date +%s%N)" -o ../bin/bifrost-desktop .

desktop-test:
	cd desktop && go test -race ./...

desktop-smoke:
	# Each GTK app needs a fresh process; Go tests may reuse a different OS thread.
	cd desktop && for test_name in TestNativeWorkspace TestNativeWorkspaceControls TestNativeCustomExtensions; do \
		BIFROST_NATIVE_TEST=1 GDK_BACKEND=x11 xvfb-run -a -s '-screen 0 3840x2160x24' go test -race -ldflags="-buildid=bifrost-native-test-$$(date +%s%N)" -run "^$$test_name$$" -count=1 -timeout=90s -v || exit $$?; \
	done
