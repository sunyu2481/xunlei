TARGET := ../../embed/authenticate_cgi/authenticate_cgi_linux_
CC_AMD64 ?= x86_64-linux-gnu-gcc
CC_ARM64 ?= aarch64-linux-gnu-gcc
READELF ?= readelf

.PHONY: build build_static amd64 arm64

build: build_static

build_static: amd64 arm64

amd64: CC = $(CC_AMD64)
amd64: MACHINE = x86_64
amd64: ELF_MACHINE = Advanced Micro Devices X86-64

arm64: CC = $(CC_ARM64)
arm64: MACHINE = aarch64
arm64: ELF_MACHINE = AArch64

# 先验证编译器目标和临时产物，避免用错误架构覆盖嵌入文件。
amd64 arm64:
	@set -eu; \
	case "$$($(CC) -dumpmachine)" in \
		$(MACHINE)*-linux-*) ;; \
		*) echo '编译器目标与 $@ 不匹配' >&2; exit 1 ;; \
	esac; \
	mkdir -p "$(dir $(TARGET))"; \
	output=$$(mktemp "$(TARGET)$@.XXXXXX"); \
	trap 'rm -f "$$output"' EXIT HUP INT TERM; \
	$(CC) main.c -o "$$output" -static -O2 -s; \
	if ! LC_ALL=C $(READELF) -h "$$output" | grep -Eq '^[[:space:]]*Machine:[[:space:]]*$(ELF_MACHINE)$$'; then \
		echo '编译产物的 ELF 架构与 $@ 不匹配' >&2; exit 1; \
	fi; \
	chmod 755 "$$output"; \
	mv "$$output" "$(TARGET)$@"
