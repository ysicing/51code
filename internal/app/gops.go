// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"log"
	"os"

	"github.com/google/gops/agent"
)

// startGopsAgent 仅在 XSOURCETREE_GOPS=1 时启动诊断。gops 端口无鉴权，本机任意进程都能
// 修改 GC、触发 profile 或导出调用栈，因此不能默认开启。
func startGopsAgent() func() {
	if os.Getenv("XSOURCETREE_GOPS") != "1" {
		return func() {}
	}
	if err := agent.Listen(agent.Options{
		Addr: "127.0.0.1:0",
	}); err != nil {
		agent.Close()
		log.Printf("启动 gops 诊断失败：%v", err)
		return func() {}
	}
	return agent.Close
}
