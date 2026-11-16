/**
 * Copyright 2026 ysicing
 * SPDX-License-Identifier: AGPL-3.0-only
 */

import { expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { needsWindowsGit, prepareWindowsGit, verifyArchive, windowsGit } from "./windows-git.mjs";

test("only Windows amd64 builds require embedded Git", () => {
  expect(needsWindowsGit([], "darwin", "arm64")).toBe(false);
  expect(needsWindowsGit([], "win32", "x64")).toBe(true);
  expect(needsWindowsGit(["-platform", "darwin/arm64,windows/amd64"])).toBe(true);
  expect(needsWindowsGit(["--platform=windows/amd64"])).toBe(true);
  expect(() => needsWindowsGit(["-platform=windows/arm64"])).toThrow("amd64");
});

test("archive checksum rejects corrupted downloads", () => {
  const data = Buffer.from("trusted");
  const hash = createHash("sha256").update(data).digest("hex");
  expect(() => verifyArchive(data, hash)).not.toThrow();
  expect(() => verifyArchive(Buffer.from("modified"), hash)).toThrow("校验失败");
});

test("non-Windows builds do not download Git", async () => {
  const cleanup = await prepareWindowsGit("/unused", ["-platform", "darwin/arm64"], {
    download: () => { throw new Error("unexpected download"); },
  });
  await cleanup();
});

test("existing resource directory is preserved", async () => {
  const root = await mkdtemp(join(tmpdir(), "51code-git-"));
  try {
    const directory = join(root, "resources", "windows-amd64", "git");
    await mkdir(directory, { recursive: true });
    await writeFile(join(directory, "keep"), "user data");
    await expect(prepareWindowsGit(root, ["-platform", "windows/amd64"])).rejects.toThrow("已存在");
    expect(await readFile(join(directory, "keep"), "utf8")).toBe("user data");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("cached archives are checked before extraction", async () => {
  const root = await mkdtemp(join(tmpdir(), "51code-git-"));
  try {
    const cache = join(root, ".mygo", "git-downloads");
    await mkdir(cache, { recursive: true });
    await writeFile(join(cache, windowsGit.url.split("/").pop()), "corrupted");
    await expect(prepareWindowsGit(root, ["-platform", "windows/amd64"], {
      download: () => { throw new Error("unexpected download"); },
      unpack: () => { throw new Error("unexpected extraction"); },
    })).rejects.toThrow("校验失败");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
