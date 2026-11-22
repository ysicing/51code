/**
 * Copyright 2026 ysicing
 * SPDX-License-Identifier: AGPL-3.0-only
 */

import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { cp, mkdir, mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { join } from "node:path";

export const windowsGit = {
  version: "2.56.1",
  url: "https://github.com/git-for-windows/git/releases/download/v2.56.1.windows.1/MinGit-2.56.1-64-bit.zip",
  sha256: "4a92850b6e917da4fd010669b614b55dccf5b0ec333a534cc4be4700dfefdafd",
};
export const windowsLFS = {
  version: "3.8.1",
  url: "https://github.com/git-lfs/git-lfs/releases/download/v3.8.1/git-lfs-windows-amd64-v3.8.1.zip",
  sha256: "5bb771c12b3dacc535e912d0270a010c209d4da2e63819c5918e3547c2defed9",
};
const lfsLicense = {
  url: "https://raw.githubusercontent.com/git-lfs/git-lfs/v3.8.1/LICENSE.md",
  sha256: "4fae9062ab5cdd5fb15486b728534d8aded8b4ae9d84b6d66a956f5162c366b6",
};

export const windowsGitFiles = [
  "cmd/git.exe", "cmd/git-lfs.exe", "usr/bin/ssh.exe", "usr/bin/sh.exe",
  "ucrt64/bin/git-credential-manager.exe", "ucrt64/etc/ssl/certs/ca-bundle.crt",
  "LICENSE.txt", "lfs-distribution/LICENSE.md",
];

export function needsWindowsGit(args, platform = process.platform, architecture = process.arch) {
  let target = `${platform === "win32" ? "windows" : platform}/${architecture === "x64" ? "amd64" : architecture}`;
  for (let index = 0; index < args.length; index++) {
    if (/^--?platform$/.test(args[index])) target = args[++index] ?? "";
    else if (/^--?platform=/.test(args[index])) target = args[index].split("=")[1];
  }
  const windows = target.split(",").filter((entry) => entry.startsWith("windows/"));
  if (windows.some((entry) => entry !== "windows/amd64")) {
    throw new Error("内置 Git 当前仅支持 Windows amd64");
  }
  return windows.length > 0;
}

export function verifyArchive(data, expected) {
  if (createHash("sha256").update(data).digest("hex") !== expected) {
    throw new Error("内置 Git 下载校验失败，停止构建");
  }
}

async function archive(cache, spec, download) {
  const path = join(cache, spec.url.split("/").pop());
  let data;
  try {
    data = await readFile(path);
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
    const response = await download(spec.url, { signal: AbortSignal.timeout(120_000) });
    if (!response.ok) throw new Error(`下载内置 Git 失败：HTTP ${response.status}`);
    data = Buffer.from(await response.arrayBuffer());
    verifyArchive(data, spec.sha256);
    await writeFile(path, data);
  }
  verifyArchive(data, spec.sha256);
  return path;
}

function extract(archivePath, destination) {
  if (process.platform === "win32") {
    execFileSync("powershell.exe", ["-NoProfile", "-NonInteractive", "-Command",
      "Expand-Archive -LiteralPath $env:CODE_GIT_ARCHIVE -DestinationPath $env:CODE_GIT_DESTINATION"], {
      env: { ...process.env, CODE_GIT_ARCHIVE: archivePath, CODE_GIT_DESTINATION: destination },
      timeout: 120_000,
    });
  } else {
    execFileSync("unzip", ["-q", archivePath, "-d", destination], { timeout: 120_000 });
  }
}

// 平台资源在 MyGo 打包前准备、结束后清理；完整保留上游证书、许可和辅助程序。
export async function prepareWindowsGit(root, args, { download = fetch, unpack = extract } = {}) {
  if (!needsWindowsGit(args)) return async () => {};
  const cache = join(root, ".mygo", "git-downloads");
  await mkdir(cache, { recursive: true });
  const destination = join(root, "resources", "windows-amd64", "git");
  try {
    await stat(destination);
    throw new Error(`内置 Git 资源目录已存在，请检查后移走：${destination}`);
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
  }
  const temporary = await mkdtemp(join(cache, "stage-"));
  let installed = false;
  const cleanup = async () => {
    if (installed) await rm(destination, { recursive: true, force: true });
    await rm(temporary, { recursive: true, force: true });
  };
  try {
    const git = await archive(cache, windowsGit, download);
    const lfs = await archive(cache, windowsLFS, download);
    const license = await archive(cache, lfsLicense, download);
    const contents = join(temporary, "git");
    await unpack(git, contents);
    await unpack(lfs, join(temporary, "lfs"));
    const lfsContents = join(temporary, "lfs", `git-lfs-${windowsLFS.version}`);
    await cp(join(lfsContents, "git-lfs.exe"), join(contents, "cmd", "git-lfs.exe"));
    await cp(lfsContents, join(contents, "lfs-distribution"), {
      recursive: true, filter: (path) => !path.endsWith("git-lfs.exe"),
    });
    await cp(license, join(contents, "lfs-distribution", "LICENSE.md"));
    for (const path of windowsGitFiles) {
      if (!(await stat(join(contents, path))).isFile()) throw new Error(`内置 Git 缺少 ${path}`);
    }
    await mkdir(join(root, "resources", "windows-amd64"), { recursive: true });
    installed = true;
    await cp(contents, destination, { recursive: true });
    return cleanup;
  } catch (error) {
    await cleanup();
    throw error;
  }
}
