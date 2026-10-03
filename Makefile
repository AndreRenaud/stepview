# Build, test and package stepview.
#
#   make              build bin/stepview and bin/stepinfo
#   make test         run the tests
#   make fuzz         run each fuzz target for FUZZTIME (default 30s)
#   make check        format, modernize (go fix) and vet the code
#   make app          build build/StepView.app
#   make install      copy the app bundle into /Applications
#   make universal    build the app bundle for both arm64 and amd64
#   make clean        remove build output

GO          ?= go
VERSION     ?= 0.1.0
APP_NAME    := StepView
BUNDLE_ID   ?= com.github.andrerenaud.stepview
# Architectures in the bundle's executable; more than one makes a universal binary.
ARCHS       ?= $(shell $(GO) env GOARCH)
# Oldest macOS the app runs on; Go 1.27 itself needs macOS 13.
MACOS_MIN   ?= 13.0
# Square image, at least 1024x1024, for the app icon.
ICON        ?= STPViewer.jpeg
# "-" signs ad hoc; set a "Developer ID Application: ..." identity to distribute.
CODESIGN_ID ?= -
INSTALL_DIR ?= /Applications

BIN   := bin
BUILD := build
APP   := $(BUILD)/$(APP_NAME).app

GO_SRC := go.mod go.sum $(shell find . \( -name '*.go' -o -name '*.m' \) -not -path './$(BUILD)/*')

# Fuzz targets as package:FuzzName; go test runs one fuzz target at a time.
FUZZTIME    ?= 30s
FUZZ_TARGETS := \
	./internal/step:FuzzParse \
	./internal/step:FuzzParseRecord \
	./internal/step:FuzzParseNumber \
	./internal/step:FuzzDecodeString \
	./internal/step:FuzzLoad \
	./internal/step:FuzzCDT \
	./internal/step:FuzzBSplineCurve \
	./internal/gltfload:FuzzLoad \
	./internal/meshload:FuzzSTL \
	./internal/meshload:FuzzOBJ \
	./internal/meshload:Fuzz3MF \
	./internal/meshload:Fuzz3DS

.PHONY: all build test fuzz check app install universal clean

all: build

build: $(BIN)/stepview $(BIN)/stepinfo

$(BIN)/stepview: $(GO_SRC) | check
	$(GO) build -o $@ .

$(BIN)/stepinfo: $(GO_SRC) | check
	$(GO) build -o $@ ./cmd/stepinfo

test: check
	$(GO) test ./...

# Failing inputs are saved under the package's testdata/fuzz directory, where
# they become regression cases for "make test".
fuzz:
	@for t in $(FUZZ_TARGETS); do \
		pkg=$${t%%:*}; name=$${t##*:}; \
		echo "== $$pkg $$name"; \
		$(GO) test -run '^$$' -fuzz "^$$name\$$" -fuzztime $(FUZZTIME) $$pkg || exit 1; \
	done

# Runs before every build and test: rewrites the code with gofmt and go fix
# (which applies the modernize fixers), then vets it.
check:
	gofmt -l -w .
	$(GO) fix ./...
	$(GO) vet ./...

# One executable per architecture. cgo is needed for the native file dialog;
# Apple's clang targets either architecture, so no cross toolchain is needed.
# Without an explicit minimum, clang targets the macOS it runs on. (Go's build
# cache keys on CGO_*FLAGS but not MACOSX_DEPLOYMENT_TARGET.)
$(BUILD)/darwin-%/stepview: $(GO_SRC) | check
	CGO_ENABLED=1 GOOS=darwin GOARCH=$* \
	CGO_CFLAGS='-O2 -g -mmacosx-version-min=$(MACOS_MIN)' CGO_LDFLAGS='-O2 -g -mmacosx-version-min=$(MACOS_MIN)' \
		$(GO) build -trimpath -ldflags='-s -w' -o $@ .

EXES := $(foreach a,$(ARCHS),$(BUILD)/darwin-$(a)/stepview)

$(BUILD)/stepview.icns: $(ICON)
	rm -rf $(BUILD)/stepview.iconset
	mkdir -p $(BUILD)/stepview.iconset
	for s in 16 32 128 256 512; do \
		sips -z $$s $$s -s format png $(ICON) --out $(BUILD)/stepview.iconset/icon_$${s}x$${s}.png >/dev/null && \
		sips -z $$((s * 2)) $$((s * 2)) -s format png $(ICON) --out $(BUILD)/stepview.iconset/icon_$${s}x$${s}@2x.png >/dev/null || exit 1; \
	done
	iconutil -c icns -o $@ $(BUILD)/stepview.iconset

# The bundle is reassembled every time so that VERSION and the like apply.
app: $(EXES) $(BUILD)/stepview.icns macos/Info.plist.in
	rm -rf $(APP)
	mkdir -p $(APP)/Contents/MacOS $(APP)/Contents/Resources
	lipo -create -output $(APP)/Contents/MacOS/stepview $(EXES)
	cp $(BUILD)/stepview.icns $(APP)/Contents/Resources/stepview.icns
	sed -e 's/@APP_NAME@/$(APP_NAME)/g' -e 's/@BUNDLE_ID@/$(BUNDLE_ID)/g' \
		-e 's/@VERSION@/$(VERSION)/g' -e 's/@MACOS_MIN@/$(MACOS_MIN)/g' macos/Info.plist.in > $(APP)/Contents/Info.plist
	plutil -lint -s $(APP)/Contents/Info.plist
	printf 'APPL????' > $(APP)/Contents/PkgInfo
	codesign --force --sign '$(CODESIGN_ID)' $(APP)
	@echo "Built $(APP)"

universal:
	$(MAKE) app ARCHS='arm64 amd64'

install: app
	rm -rf '$(INSTALL_DIR)/$(APP_NAME).app'
	ditto $(APP) '$(INSTALL_DIR)/$(APP_NAME).app'
	@echo "Installed $(INSTALL_DIR)/$(APP_NAME).app"

clean:
	rm -rf $(BIN) $(BUILD)
