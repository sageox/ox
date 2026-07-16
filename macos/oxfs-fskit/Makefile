# oxfs-fskit — pure-Swift core build/test (no Xcode required).
# The FSKit .appex adapter is a separate Xcode target (needs Xcode + signing)
# and is not built here.
.PHONY: build test run clean

build:
	swift build

# Uses scripts/test.sh so swift-testing works on a CLT-only box.
test:
	./scripts/test.sh

run:
	swift run oxfs-hello

clean:
	swift package clean
	rm -rf .build
