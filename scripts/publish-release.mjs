/**
 * Copyright 2026 ysicing
 * SPDX-License-Identifier: AGPL-3.0-only
 */

import { createHash, createPublicKey, verify } from "node:crypto";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { spawn, spawnSync } from "node:child_process";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { windowsPortableName } from "./windows-portable.mjs";

const sha256 = (data) => createHash("sha256").update(data).digest("hex");
const maxManifestBytes = 1024 * 1024;

// 一次 mygo 调用构建全部平台，共用构建号；每个平台都带签名更新包与清单。
export const updateTargets = ["darwin-arm64", "darwin-amd64", "windows-amd64", "linux-amd64", "linux-arm64"];

export async function releasePlan(root, directory = "build") {
  const { version } = JSON.parse(await readFile(resolve(root, "package.json"), "utf8"));
  const config = JSON.parse(await readFile(resolve(root, "release.config.json"), "utf8"));
  if (!/^[\w-]+\/[\w.-]+$/.test(config.githubRepository ?? "")) {
    throw new Error("发布配置缺少有效的 GitHub owner/repository");
  }
  if (!/^[a-zA-Z0-9][a-zA-Z0-9_-]*$/.test(config.gemfuryAccount ?? "")) {
    throw new Error("发布配置缺少有效的 Gemfury 组织名 gemfuryAccount");
  }
  const base = new URL(config.downloadBase);
  if (!/^\d+\.\d+\.\d+$/.test(version) || base.protocol !== "https:" ||
      base.username || base.password || base.search || base.hash ||
      !/^[\w.-]+\/[\w./-]+$/.test(config.storageTarget) ||
      config.storageTarget.split("/").some((part) => part === "." || part === "..")) {
    throw new Error("发布版本必须为稳定版，下载地址须为 HTTPS，存储目标须为 mc 别名下的路径");
  }
  const downloadBase = base.href.replace(/\/$/, "");
  const storageTarget = config.storageTarget.replace(/\/$/, "");
  const publicKey = Buffer.from(config.publicKey, "base64");
  if (publicKey.length !== 32) throw new Error("更新公钥无效");
  const key = createPublicKey({
    key: Buffer.concat([Buffer.from("302a300506032b6570032100", "hex"), publicKey]),
    format: "der", type: "spki",
  });
  const artifacts = [];
  const feeds = [];
  let notes;
  const artifact = (name, path, data) => ({
    name, path, hash: sha256(data),
    object: `${storageTarget}/${name}`,
    url: `${downloadBase}/${name}`,
  });
  // 全平台共用更新源：每个平台的清单都须指向 CDN 上的归档，并用同一私钥签名。
  for (const target of updateTargets) {
    const folder = resolve(root, directory, target);
    const manifestName = `update-${target}.json`;
    const manifestPath = resolve(folder, manifestName);
    const manifestData = await readFile(manifestPath);
    const manifest = JSON.parse(manifestData);
    notes ??= manifest.notes;
    const archiveName = `51code-${version}-${target}.tar.gz`;
    const archivePath = resolve(folder, archiveName);
    const archive = await readFile(archivePath);
    const signature = Buffer.from(manifest.signature ?? "", "base64");
    if (manifest.version !== version || manifest.url !== `${downloadBase}/${archiveName}` ||
        manifest.size !== archive.length || !manifest.notes?.trim() || manifest.deltas?.length ||
        signature.length !== 64 ||
        !verify(null, createHash("sha256").update(archive).digest(), key, signature)) {
      throw new Error(`${target} 的清单、签名或更新包不一致，请用当前配置和签名私钥重新构建`);
    }
    artifacts.push(artifact(archiveName, archivePath, archive));
    const macOS = target.startsWith("darwin-");
    if (macOS) {
      const dmgName = `51code-${version}-${target.slice("darwin-".length)}.dmg`;
      const dmgPath = resolve(folder, dmgName);
      const dmg = await readFile(dmgPath);
      if (!dmg.length) throw new Error(`${dmgName} 为空`);
      artifacts.push(artifact(dmgName, dmgPath, dmg));
    }
    artifacts.push(artifact(`${version}/${manifestName}`, manifestPath, manifestData));
    feeds.push({ ...artifact(manifestName, manifestPath, manifestData), version });
  }
  // Linux 推荐 `curl …/install.sh | sh` 安装到用户目录以便自更新。脚本内嵌更新地址和公钥，
  // 固定地址随每次发布覆盖；各平台目录是同一份，内容不一致或未内嵌当前配置时拒绝发布。
  const scripts = [];
  const installPath = resolve(root, directory, "linux-amd64", "install.sh");
  const install = await readFile(installPath);
  const installText = install.toString();
  if (!install.equals(await readFile(resolve(root, directory, "linux-arm64", "install.sh"))) ||
      !installText.startsWith("#!/bin/sh\n") || !installText.includes(`'${config.publicKey}'`) ||
      !installText.includes(`'${downloadBase}/'`)) {
    throw new Error("install.sh 与当前更新地址或公钥不一致，请用当前配置重新构建");
  }
  scripts.push(artifact("install.sh", installPath, install));
  // GitHub Release 附件额外包含 Windows 安装器、免安装 ZIP 和 Linux deb/rpm。
  const packages = artifacts.filter((file) => file.name.endsWith(".dmg"));
  const deb = Buffer.from("!<arch>\ndebian-binary");
  const rpm = Buffer.from([0xed, 0xab, 0xee, 0xdb]);
  for (const [target, name, magic] of [
    ["windows-amd64", `51code-setup-${version}-amd64.exe`, Buffer.from("MZ")],
    ["windows-amd64", windowsPortableName(version), Buffer.from("504b0304", "hex")],
    // 文件名沿用 mygo 的命名：deb 用 Debian 架构名，rpm 用 RPM 架构名且 release 固定为 1。
    ...[["amd64", "x86_64"], ["arm64", "aarch64"]].flatMap(([arch, rpmArch]) => [
      [`linux-${arch}`, `51code_${version}_${arch}.deb`, deb],
      [`linux-${arch}`, `51code-${version}-1.${rpmArch}.rpm`, rpm],
    ]),
  ]) {
    const path = resolve(root, directory, target, name);
    const data = await readFile(path);
    // 检查格式头，防止空文件或误放的其他平台产物被上传。
    if (!data.subarray(0, magic.length).equals(magic)) {
      throw new Error(`${name} 不是有效的${name.endsWith(".zip") ? "免安装 ZIP" : "安装包"}，请重新构建`);
    }
    packages.push({ name, path, hash: sha256(data) });
  }
  return {
    version, artifacts, feeds, scripts,
    github: { repository: config.githubRepository, notes, packages },
    gemfury: { account: config.gemfuryAccount, packages: packages.filter((file) => /\.(deb|rpm)$/.test(file.name)) },
  };
}

export async function verifyAppleRelease(root, directory, plan, run = spawnSync) {
  const { macos } = JSON.parse(await readFile(resolve(root, "release.config.json"), "utf8"));
  if (!/^[A-Z0-9]{10}$/.test(macos?.teamID ?? "")) throw new Error("发布配置缺少有效的 Apple 团队 ID");
  const checked = (program, args) => {
    const result = run(program, args, { encoding: "utf8", timeout: 120_000, maxBuffer: 1024 * 1024 });
    if (result.error || result.status !== 0) {
      throw new Error(`Apple 发布校验失败（${program} ${args.join(" ")}）：${result.error?.message ?? result.stderr ?? result.stdout}`);
    }
    return `${result.stdout ?? ""}\n${result.stderr ?? ""}`;
  };
  const temporary = await mkdtemp(resolve(tmpdir(), "51code-signing-"));
  try {
    for (const arch of ["arm64", "amd64"]) {
      const folder = resolve(root, directory, `darwin-${arch}`);
      const extracted = resolve(temporary, arch);
      await mkdir(extracted);
      checked("tar", ["-xzf", resolve(folder, `51code-${plan.version}-darwin-${arch}.tar.gz`), "-C", extracted]);
      const app = resolve(extracted, "51Code.app");
      const dmg = resolve(folder, `51code-${plan.version}-${arch}.dmg`);
      for (const artifact of [app, dmg]) {
        checked("codesign", ["--verify", "--deep", "--strict", artifact]);
        const signature = checked("codesign", ["--display", "--verbose=4", artifact]);
        if (!/^Authority=Developer ID Application:/m.test(signature) ||
            !signature.split(/\r?\n/).includes(`TeamIdentifier=${macos.teamID}`) ||
            (artifact === app && !/flags=.*\bruntime\b/.test(signature))) {
          throw new Error(`${artifact} 必须使用指定团队的 Developer ID 签名，应用须启用 Hardened Runtime`);
        }
        checked("xcrun", ["stapler", "validate", artifact]);
        checked("spctl", artifact === app
          ? ["--assess", "--type", "execute", artifact]
          : ["--assess", "--type", "open", "--context", "context:primary-signature", artifact]);
      }
    }
  } finally {
    await rm(temporary, { recursive: true, force: true });
  }
}

async function command(args, hashOutput = false) {
  return new Promise((resolveResult, reject) => {
    const child = spawn("mc", args, { stdio: ["ignore", "pipe", "ignore"], timeout: 300_000 });
    const digest = createHash("sha256");
    const chunks = [];
    const outputLimit = args[0] === "cat" ? maxManifestBytes : 256 * 1024;
    let size = 0;
    child.stdout.on("data", (chunk) => {
      if (hashOutput) digest.update(chunk);
      else if ((size += chunk.length) <= outputLimit) chunks.push(chunk);
      else {
        child.kill();
        reject(new Error(`mc ${args[0]} 输出超过 ${outputLimit} 字节`));
      }
    });
    child.on("error", () => reject(new Error("无法运行 mc，请先安装并配置对象存储别名")));
    child.on("close", (status) => resolveResult({
      status, text: Buffer.concat(chunks).toString(), hash: digest.digest("hex"),
    }));
  });
}

async function onlineHash(url) {
  const response = await fetch(url, { cache: "no-store", signal: AbortSignal.timeout(300_000) });
  if (!response.ok) throw new Error(`CDN 回读失败：${url}（HTTP ${response.status}）`);
  const digest = createHash("sha256");
  for await (const chunk of response.body) digest.update(chunk);
  return digest.digest("hex");
}

function missing(result) {
  try {
    const state = JSON.parse(result.text);
    return state.status === "error" && state.error?.cause?.message === "Object does not exist";
  } catch {
    return false;
  }
}

function newer(left, right) {
  const first = left.split(".").map(Number);
  const second = right.split(".").map(Number);
  for (let index = 0; index < 3; index++) {
    if (first[index] !== second[index]) return first[index] > second[index];
  }
  return false;
}

export async function addReleaseHistory(root, directory) {
  const { version } = JSON.parse(await readFile(resolve(root, "package.json"), "utf8"));
  const changelog = await readFile(resolve(root, "CHANGELOG.md"), "utf8");
  const headings = [...changelog.matchAll(/^##[ \t]+([^\r\n]+?)[ \t]*\r?$/gm)];
  const history = {};
  for (const [index, heading] of headings.entries()) {
    const previous = heading[1];
    if (!/^\d+\.\d+\.\d+$/.test(previous) || !newer(version, previous)) continue;
    const notes = changelog.slice(heading.index + heading[0].length, headings[index + 1]?.index).trim();
    if (notes) history[previous] = notes;
  }
  for (const target of updateTargets) {
    const path = resolve(root, directory, target, `update-${target}.json`);
    const manifest = JSON.parse(await readFile(path, "utf8"));
    if (manifest.version !== version) throw new Error(`${target} 的清单版本与构建版本不一致`);
    const data = JSON.stringify({ ...manifest, releaseHistory: history }, null, 2) + "\n";
    if (Buffer.byteLength(data) > maxManifestBytes) throw new Error(`${target} 的更新清单超过 MyGo 的 1 MiB 限制`);
    await writeFile(path, data);
  }
}

export async function publish(plan, { run = command, remoteHash = onlineHash } = {}) {
  const existing = new Set();
  for (const file of plan.artifacts) {
    const stat = await run(["stat", "--json", file.object]);
    if (stat.status === 0) {
      const current = await run(["cat", file.object], true);
      if (current.status !== 0 || current.hash !== file.hash) {
        throw new Error(`禁止覆盖已发布的 ${file.name}；构建变化须推进版本号`);
      }
      existing.add(file.object);
    } else if (!missing(stat)) {
      throw new Error(`无法确认 ${file.name} 的源站状态，停止发布`);
    }
  }
  for (const feed of plan.feeds) {
    const stat = await run(["stat", "--json", feed.object]);
    if (stat.status === 0) {
      const current = await run(["cat", feed.object]);
      if (current.status !== 0) throw new Error("读取当前更新清单失败");
      const { version } = JSON.parse(current.text);
      if (!/^\d+\.\d+\.\d+$/.test(version) || newer(version, plan.version)) {
        throw new Error("当前更新源版本更新或无效，禁止回退");
      }
    } else if (!missing(stat)) {
      throw new Error("无法确认当前更新清单，停止发布");
    }
  }
  for (const file of plan.artifacts) {
    if (!existing.has(file.object)) {
      const result = await run([
        "--custom-header", "If-None-Match: *", "cp", "--disable-multipart", "--quiet", "--json",
        "--attr", "Cache-Control=public,max-age=31536000,immutable", file.path, file.object,
      ]);
      if (result.status !== 0) throw new Error(`条件上传 ${file.name} 失败，请核实源站后重试`);
    }
    if (await remoteHash(file.url) !== file.hash) {
      throw new Error(`CDN 上的 ${file.name} 校验不一致，未发布更新清单`);
    }
  }
  // install.sh 与固定清单一样可覆盖，放在清单之后：脚本生效时清单已经指向新版本。
  for (const feed of [...plan.feeds, ...plan.scripts]) {
    const result = await run([
      "cp", "--disable-multipart", "--quiet", "--json",
      "--attr", "Cache-Control=no-cache,max-age=0,must-revalidate", feed.path, feed.object,
    ]);
    if (result.status !== 0) throw new Error(`发布 ${feed.name} 失败`);
    if (await remoteHash(feed.url) !== feed.hash) {
      throw new Error(`${feed.name} 的 CDN 回读不一致，请检查 CDN 缓存规则后重试`);
    }
  }
}

export async function publishGitHubRelease(plan, { run = spawnSync } = {}) {
  const tag = `v${plan.version}`;
  const repository = plan.github.repository;
  const repoArgs = ["--repo", `github.com/${repository}`];
  const invoke = (args, input) => {
    const result = run("gh", args, {
      encoding: "utf8", timeout: 300_000, maxBuffer: 4 * 1024 * 1024,
      input, env: { ...process.env, GH_PROMPT_DISABLED: "1" },
    });
    if (result.error || result.status !== 0) {
      throw new Error(`GitHub Release 操作失败：${result.error?.message ?? result.stderr ?? result.stdout}`);
    }
    return result.stdout;
  };
  const readRelease = () => {
    const response = invoke([
      "api", "--hostname", "github.com", "--paginate", "--jq",
      `.[] | select(.tag_name == ${JSON.stringify(tag)}) | {tag_name, draft, prerelease, assets: [.assets[] | {name, state, digest}]}`,
      `repos/${repository}/releases?per_page=100`,
    ]);
    if (!response.trim()) return null;
    const current = JSON.parse(response);
    if (current.tag_name !== tag || current.prerelease !== false ||
        typeof current.draft !== "boolean" || !Array.isArray(current.assets)) {
      throw new Error("GitHub Release 的版本或状态不符合稳定版发布要求");
    }
    return current;
  };
  const { packages } = plan.github;
  const verifyAsset = (file, asset) => {
    if (asset?.state === "starter") {
      throw new Error(`GitHub 附件 ${file.name} 是上传失败留下的 starter 占位文件；请在 ${repository} 的 ${tag} Release 中核实并手动删除该占位附件，再用同一产物目录重试；脚本不会删除或覆盖附件`);
    }
    if (!asset || asset.state !== "uploaded" || asset.digest !== `sha256:${file.hash}`) {
      throw new Error(`GitHub 附件 ${file.name} 的 SHA-256 不一致或无法验证，禁止覆盖，请核实后重试`);
    }
  };
  invoke(["api", "--hostname", "github.com", `repos/${repository}/git/ref/tags/${tag}`]);
  let current = readRelease();
  const pending = packages.filter((file) => {
    const existing = current?.assets.find((asset) => asset.name === file.name);
    if (existing) verifyAsset(file, existing);
    return !existing;
  });
  if (!current) {
    invoke([
      "release", "create", tag, ...repoArgs, "--verify-tag", "--draft",
      "--title", `51Code ${tag}`, "--notes-file", "-",
    ], plan.github.notes);
  }
  for (const file of pending) {
    invoke(["release", "upload", tag, file.path, ...repoArgs]);
  }
  current = readRelease();
  for (const file of packages) {
    verifyAsset(file, current?.assets.find((asset) => asset.name === file.name));
  }
  if (current.draft) invoke(["release", "edit", tag, ...repoArgs, "--draft=false", "--verify-tag"]);
  current = readRelease();
  if (!current || current.draft) throw new Error("GitHub Release 仍未公开发布，请使用同一产物重试");
  for (const file of packages) {
    verifyAsset(file, current.assets.find((asset) => asset.name === file.name));
  }
  return `https://github.com/${repository}/releases/tag/${tag}`;
}

export async function publishGemfuryRelease(plan, { run = spawnSync, dryRun = false } = {}) {
  const { account, packages } = plan.gemfury;
  const common = ["--account", account, "--no-input", "--no-progress"];
  if (dryRun) {
    console.log(`随后发布 Gemfury 组织 ${account} 的公开 deb/rpm，已存在文件须通过 SHA-256 校验后复用：`);
    console.log(`fury push --account ${account} --public --no-input --no-progress ${packages.map((file) => JSON.stringify(file.path)).join(" ")}`);
    return;
  }
  const invoke = (args) => {
    const result = run("fury", [...args, ...common], {
      encoding: "utf8", timeout: args[0] === "push" ? 660_000 : 120_000, maxBuffer: 4 * 1024 * 1024,
      stdio: ["ignore", "pipe", "pipe"], env: { ...process.env },
    });
    if (result.error || result.status !== 0) {
      throw new Error(`Gemfury 操作失败，请确认 fury CLI 和 ${account} 的登录权限：${result.error?.message ?? result.stderr ?? result.stdout}`);
    }
    return result.stdout;
  };
  const listing = (args) => {
    const records = JSON.parse(invoke([...args, "--json"]));
    if (!Array.isArray(records)) throw new Error("Gemfury JSON 输出无效，请使用支持 --json 的 fury CLI");
    return records;
  };
  const verifiedFiles = () => {
    const entries = listing(["packages"]);
    const verified = new Set();
    for (const kind of ["deb", "rpm"]) {
      const matching = entries.filter((entry) => entry?.name === "51code" && entry.kind_key === kind);
      if (matching.length > 1) throw new Error(`Gemfury 中存在多个 51code ${kind} 包，停止发布`);
      const entry = matching[0];
      if (!entry) continue;
      if (typeof entry.id !== "string" || !entry.id || entry.private !== false) {
        throw new Error(`请先在 Gemfury 的 ${account} 组织中将 51code ${kind} 包设为公开，再用同一产物目录重试；--public 仅用于新建公开包`);
      }
      const versions = listing(["versions", entry.id]);
      for (const file of packages.filter((file) => file.name.endsWith(`.${kind}`))) {
        const existing = versions.filter((version) => version?.filename === file.name);
        if (existing.length > 1 || existing.length === 1 && existing[0].digests?.sha256 !== file.hash) {
          throw new Error(`Gemfury 中 ${file.name} 的 SHA-256 不一致或无法验证，禁止覆盖`);
        }
        if (existing.length === 1) verified.add(file.name);
      }
    }
    return verified;
  };
  for (const file of packages) {
    if (sha256(await readFile(file.path)) !== file.hash) {
      throw new Error(`Gemfury 上传前 ${file.name} 已变化，请保留原发布产物，不要重建同版本包`);
    }
  }
  const existing = verifiedFiles();
  const pending = packages.filter((file) => !existing.has(file.name));
  if (pending.length > 0) {
    invoke(["push", "--public", ...pending.map((file) => file.path)]);
    const published = verifiedFiles();
    if (packages.some((file) => !published.has(file.name))) {
      throw new Error("Gemfury 上传后缺少可验证的安装包，请用同一产物目录重试");
    }
  }
  return `https://repo.fury.io/${account}/`;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const args = process.argv.slice(2).filter((value) => value !== "--");
    const directoryIndex = args.indexOf("--dir");
    if (args.some((value, index) => !["--dry-run", "--dir", "--build"].includes(value) &&
          !(directoryIndex >= 0 && index === directoryIndex + 1)) ||
        (directoryIndex >= 0 && !args[directoryIndex + 1])) {
      throw new Error("用法：bun run publish [--dry-run] [--dir build]；bun run release [--dry-run]");
    }
    const root = fileURLToPath(new URL("../", import.meta.url));
    let directory = directoryIndex < 0 ? "build" : args[directoryIndex + 1];
    if (args.includes("--build")) {
      if (directoryIndex >= 0) throw new Error("--build 与 --dir 不能同时使用");
      if (!process.env.MYGO_UPDATER_PRIVATE_KEY?.trim()) throw new Error("请先设置 MYGO_UPDATER_PRIVATE_KEY");
      await mkdir(resolve(root, "build"), { recursive: true });
      directory = await mkdtemp(resolve(root, "build/release-"));
      const platforms = updateTargets.map((target) => target.replace("-", "/")).join(",");
      const build = spawnSync("bun", ["run", "build", "--", "-platform", platforms, "-o", directory], {
        cwd: root, stdio: "inherit",
        env: {
          ...process.env, XSOURCETREE_RELEASE: "1", GOMAXPROCS: "1",
          GOFLAGS: `${process.env.GOFLAGS ?? ""} -p=1`.trim(),
        },
      });
      if (build.error || build.status !== 0) throw new Error("发布构建失败，未上传");
      await addReleaseHistory(root, directory);
      console.log(`发布产物：${directory}`);
    }
    const plan = await releasePlan(root, directory);
    await verifyAppleRelease(root, directory, plan);
    if (args.includes("--dry-run")) {
      console.log(`已校验 ${plan.version} 的全平台更新签名，以及 macOS 的 Developer ID、公证与 Gatekeeper；将先上传并回读以下对象，再发布更新清单和 install.sh：`);
      console.log([...plan.artifacts, ...plan.feeds, ...plan.scripts].map((file) => file.object).join("\n"));
      console.log(`随后发布 GitHub Release：${plan.github.repository} v${plan.version}，附加以下安装包和免安装包：`);
      console.log(plan.github.packages.map((file) => file.name).join("\n"));
      await publishGemfuryRelease(plan, { dryRun: true });
    } else {
      await publish(plan);
      console.log(`已发布 51Code ${plan.version}，全平台 CDN 文件、更新清单及 install.sh 回读通过`);
      try {
        const url = await publishGitHubRelease(plan);
        console.log(`GitHub Release、全平台安装包和 Windows 免安装包已发布并校验：${url}`);
      } catch (error) {
        throw new Error(`CDN 与更新清单已发布，GitHub Release 尚未完成；请保留同一产物目录重试：${error.message}`);
      }
      try {
        const url = await publishGemfuryRelease(plan);
        console.log(`Gemfury 的公开 deb/rpm 已发布并校验：${url}`);
      } catch (error) {
        throw new Error(`CDN、更新清单和 GitHub Release 已发布，Gemfury 尚未完成；请保留同一产物目录重试：${error.message}`);
      }
    }
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  }
}
