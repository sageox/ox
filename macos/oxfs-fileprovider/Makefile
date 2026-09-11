APP_NAME := OxfsFileProviderDev.app
BUILD_APP := .derived/Build/Products/Debug/OxfsFileProviderHost.app
INSTALL_APP := /Applications/$(APP_NAME)

.PHONY: project build install run clean-derived

project:
	xcodegen generate

build: project
	xcodebuild \
		-project OxfsFileProvider.xcodeproj \
		-scheme OxfsFileProviderHost \
		-configuration Debug \
		-derivedDataPath .derived \
		CODE_SIGN_STYLE=Automatic \
		build

install: build
	ditto "$(BUILD_APP)" "$(INSTALL_APP)"
	/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister \
		-f "$(INSTALL_APP)"
	pluginkit -a "$(INSTALL_APP)/Contents/PlugIns/OxfsFileProviderExtension.appex"
	pluginkit -e use -i ai.sageox.oxfs.fileprovider.extension

run: install
	open -n "$(INSTALL_APP)"
