/**
 * Copyright 2026 ysicing
 * SPDX-License-Identifier: AGPL-3.0-only
 */

import { execFileSync } from "node:child_process";
import { lstat, mkdtemp, readFile, rename, rm } from "node:fs/promises";
import { join } from "node:path";
import { windowsGitFiles } from "./windows-git.mjs";

export const windowsPortableName = (version) => `51code-${version}-windows-amd64-portable.zip`;

export async function buildWindowsPortable(directory, version) {
  const fileList = ".mygo-files";
  const entries = (await readFile(join(directory, fileList), "utf8")).replace(/\r?\n$/, "").split(/\r?\n/);
  if (!entries.includes("51Code.exe") || !entries.includes("git") ||
      entries.some((name) => !name || name === "." || name === ".." || name === fileList ||
        /^[-@]|[/\\:\x00\r\n]/.test(name))) {
    throw new Error("MyGo 的 Windows 应用文件清单无效，无法生成免安装包");
  }
  for (const name of ["51Code.exe", ...windowsGitFiles.map((path) => `git/${path}`)]) {
    const file = await lstat(join(directory, name)).catch((error) => {
      if (error.code !== "ENOENT") throw error;
      return null;
    });
    if (!file?.isFile() || file.size === 0) throw new Error(`Windows 免安装包缺少 ${name}`);
  }

  const name = windowsPortableName(version);
  const temp = await mkdtemp(join(directory, ".portable-"));
  try {
    const archive = join(temp, name);
    const options = { cwd: directory, stdio: "inherit", timeout: 120_000 };
    if (process.platform === "win32") {
      const tar = join(process.env.SystemRoot ?? "C:\\Windows", "System32", "tar.exe");
      execFileSync(tar, ["-caf", archive, "--", fileList, ...entries], options);
    } else {
      execFileSync("zip", ["-qrX", archive, fileList, ...entries], options);
    }
    await rename(archive, join(directory, name));
    console.log(`Windows 免安装包：${join(directory, name)}`);
  } finally {
    await rm(temp, { recursive: true, force: true });
  }
}
