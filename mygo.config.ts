/**
 * Copyright 2026 ysicing
 * SPDX-License-Identifier: AGPL-3.0-only
 */

import { defineConfig } from "mygo-cli";
import { version } from "./package.json";
import release from "./release.config.json";

export default defineConfig({
  name: "51Code",
  slug: "51code",
  identifier: "com.ysicing.xsourcetree",
  version,
  out: "build",
  i18n: { dir: "locales", source: "zh-Hans" },
  macos: {
    minimumSystemVersion: "12.0",
    ...(process.env.XSOURCETREE_BUILD_NUMBER ? {
      infoPlist: {
        CFBundleVersion: process.env.XSOURCETREE_BUILD_NUMBER,
        XSourceTreeCommit: process.env.XSOURCETREE_BUILD_COMMIT,
        XSourceTreeBuildTime: process.env.XSOURCETREE_BUILD_TIME,
      },
    } : {}),
    ...(process.env.XSOURCETREE_RELEASE === "1" ? {
      signingIdentity: release.macos.signingIdentity,
      notarize: { keychainProfile: release.macos.notaryProfile },
    } : {}),
  },
  // 原生界面无需 WebKitGTK；Git 和 Secret Service 是 Linux 运行时依赖。
  // 按格式显式声明依赖：mygo 0.9.0 的 Debian→RPM 映射不认识 fonts-noto-cjk，会从 RPM 依赖中丢弃。
  // RPM 用 SONAME 与 font() 能力而非包名，兼容 Fedora、RHEL 系和 openSUSE 的不同包名。
  linux: {
    formats: ["deb", "rpm"],
    webview: false,
    command: "51code",
    categories: ["Development"],
    depends: {
      deb: ["git", "libsecret-1-0", "fonts-noto-cjk"],
      rpm: ["git", "libsecret-1.so.0()(64bit)", "font(notosanscjksc)"],
    },
  },
  // Windows 未配置证书或 signCommand，安装包不提供 Authenticode 签名；更新包仍由 Ed25519 验签。
  // 全平台共用更新源，各平台只读自己的 update-<os>-<arch>.json。安装目录不可写时（deb/rpm 装在 /opt）
  // mygo 不自更新，应用改为引导到 GitHub Releases 手动下载。
  updates: {
    publicKey: release.publicKey,
    url: release.downloadBase,
    deltas: 0,
  },
});
