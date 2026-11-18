#!/bin/sh
# 构建插件可执行文件到 bin/
set -e
cd "$(dirname "$0")"
go build -o bin/paperplane-plugin-spring-boot ./cmd/paperplane-plugin-spring-boot
