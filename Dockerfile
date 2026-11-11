ARG TARGETARCH
FROM --platform=linux/${TARGETARCH} ubuntu:focal
ARG TARGETARCH
ENV DEBIAN_FRONTEND=noninteractive

# 保持 BusyBox 的 /lib64 符号链接布局，避免动态链接器目录冲突。
RUN apt-get update \
  && apt-get install --no-install-recommends -y ca-certificates tzdata iproute2 \
  && rm -rf /var/lib/apt/lists/* \
  && mkdir -p /rootfs/etc/ssl/certs /rootfs/lib \
  && ln -s lib /rootfs/lib64 \
  && find /usr/lib \( -name libdl.so.2 -o -name libgcc_s.so.1 -o -name libstdc++.so.6 \) -exec cp -Lr {} /rootfs/lib/ \; \
  && cp -Lr /usr/share/zoneinfo/Asia/Chongqing /rootfs/etc/localtime \
  && echo "Asia/Chongqing" >/rootfs/etc/timezone \
  && cp -Lr --parents /etc/ssl/certs/ca-certificates.crt /rootfs/ \
  && mkdir -p /rootfs/sbin \
  && cp -L /sbin/ip /sbin/tc /rootfs/sbin/ \
  && ldd /sbin/ip /sbin/tc | awk '$2 == "=>" { print $3 } $1 ~ /^\// && $1 !~ /:$/ { print $1 }' \
    | sort -u | xargs -r cp -L --parents --target-directory=/rootfs

COPY artifacts/xlp-${TARGETARCH} /rootfs/xlp
RUN chmod +x /rootfs/xlp

# 使用 musl 版本，避免 Ubuntu 的 glibc 运行库影响 BusyBox 启动。
FROM --platform=linux/${TARGETARCH} busybox:1.37-musl
ARG TARGETARCH

LABEL org.opencontainers.image.authors=cnk3x \
  org.opencontainers.image.source=https://github.com/cnk3x/xunlei \
  org.opencontainers.image.description="迅雷远程下载服务(非官方)" \
  org.opencontainers.image.licenses=MIT

COPY --from=0 /rootfs /

ENV \
  XL_DASHBOARD_PORT=2345 \
  XL_DASHBOARD_IP= \
  XL_DASHBOARD_USERNAME= \
  XL_DIR_DOWNLOAD=/xunlei/downloads \
  XL_PREVENT_UPDATE= \
  XL_UID= \
  XL_GID= \
  XL_DEBUG= \
  XL_UPLOAD_LIMIT=0 \
  XL_UPLOAD_INTERFACE=eth0 \
  XL_SPK_URL=

VOLUME [ "/xunlei/data", "/xunlei/var/packages/pan-xunlei-com" ]
EXPOSE 2345

CMD [ "/xlp" ]
