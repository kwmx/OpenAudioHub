VERSION ?= 0.1.17

.PHONY: test build release zip

test:
	go test ./...

build:
	go build -ldflags "-X main.version=$(VERSION)" -o build/openaudiohubd ./cmd/openaudiohubd

release: test
	mkdir -p release
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o release/openaudiohubd-linux-arm64 ./cmd/openaudiohubd
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o release/openaudiohubd-linux-amd64 ./cmd/openaudiohubd

zip: release
	cd .. && zip -qr OpenAudioHub-$(VERSION).zip OpenAudioHub -x 'OpenAudioHub/.git/*'
