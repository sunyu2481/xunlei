# 本地开发构建；发布流程使用 GitHub Actions。
NAME := xlp
TARGETARCH ?= $(shell go env GOHOSTARCH)
TARGETARCH := $(TARGETARCH)

ifneq ($(TARGETARCH),amd64)
ifneq ($(TARGETARCH),arm64)
$(error TARGETARCH 仅支持 amd64 或 arm64)
endif
endif

.PHONY: showTag amd64 arm64 build busybox ubuntu debian

http_proxy := http://host.docker.internal:7890
https_proxy := http://host.docker.internal:7890

GBuild := CGO_ENABLED=0 GOOS=linux go build -v -ldflags '-s -w'
DProxy := --build-arg http_proxy=$(http_proxy) --build-arg https_proxy=$(https_proxy)
DBuild := docker buildx build
DPush := $(DBuild) --push --platform linux/amd64,linux/arm64

VERSION := "$(shell cat xlp.go | grep "const Version =" | head -n1 | grep -Eo '"[^"]+"' | sed 's/"//g')beta"

showTag::
	@echo version is $(VERSION)

amd64 arm64:
	GOARCH=$@ $(GBuild) -o artifacts/xlp-$@ ./cmd/xlp
	tar -C artifacts --transform='s/^xlp-$@$$/xlp/' -czvf artifacts/xlp-$(VERSION)-linux-$@.tar.gz xlp-$@

build:: amd64 arm64

busybox:: $(TARGETARCH)
	$(DBuild) --load --platform linux/$(TARGETARCH) -t $(NAME):$(VERSION) .

ubuntu:: $(TARGETARCH)
	$(DBuild) --load --platform linux/$(TARGETARCH) -t $(NAME):$(VERSION)-ubuntu -f ubuntu.Dockerfile .

debian:: $(TARGETARCH)
	$(DBuild) --load --platform linux/$(TARGETARCH) -t $(NAME):$(VERSION)-debian -f debian.Dockerfile .
