// paperplane-plugin-spring-boot 是 spring-boot 插件的独立可执行入口。
package main

import (
	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"

	springboot "github.com/lemon-paperplane/lemon-paperplane-plugin-spring-boot/plugin"
)

func main() { sdk.Serve(springboot.New()) }
