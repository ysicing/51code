// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"log"
	"os"

	"github.com/google/gops/agent"
)

func startGopsAgent() func() {
	if os.Getenv("XSOURCETREE_GOPS") == "0" {
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
