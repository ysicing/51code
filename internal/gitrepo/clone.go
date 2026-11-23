// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
)

// SuggestCloneDirectoryName 从远程地址推导符合克隆校验的单层目录名。
func SuggestCloneDirectoryName(source string) string {
	source = strings.TrimSpace(source)
	if strings.HasPrefix(source, "-") || strings.ContainsFunc(source, unicode.IsControl) {
		return ""
	}

	var name string
	if strings.Contains(source, "://") {
		remote, err := url.Parse(source)
		if err != nil || remote.Hostname() == "" {
			return ""
		}
		switch remote.Scheme {
		case "http", "https", "ssh":
		default:
			return ""
		}
		name, err = url.PathUnescape(path.Base(strings.TrimRight(remote.EscapedPath(), "/")))
		if err != nil {
			return ""
		}
	} else {
		separator := strings.IndexByte(source, ':')
		if bracket := strings.IndexByte(source, '['); bracket >= 0 && bracket < separator {
			end := strings.IndexByte(source, ']')
			if end < separator || !strings.HasPrefix(source[end+1:], ":") {
				return ""
			}
			separator = end + 1
		}
		if separator <= 0 || strings.ContainsAny(source[:separator], `/\ `) || source[separator-1] == '@' {
			return ""
		}
		name = path.Base(strings.TrimRight(source[separator+1:], "/"))
	}

	name = strings.TrimSuffix(name, ".git")
	if err := validateCloneDirectoryName(name); err != nil {
		return ""
	}
	return name
}

// Clone 将仓库克隆到已存在父目录下的新目录，不覆盖已有目标。
func Clone(ctx context.Context, source, parent, name string) (*Repository, error) {
	if err := validateClone(source, parent, name); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parent, err := filepath.Abs(parent)
	if err != nil {
		return nil, err
	}
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		return nil, fmt.Errorf("无法访问克隆父目录：%w", err)
	}
	// Git 在临时目录中运行，相对本地源必须在切换目录前解析。
	if !strings.Contains(source, ":") && !filepath.IsAbs(source) {
		source, err = filepath.Abs(source)
		if err != nil {
			return nil, err
		}
	}
	target := filepath.Join(parent, name)
	if err := os.Mkdir(target, 0755); err != nil {
		return nil, fmt.Errorf("无法创建克隆目录（目标必须不存在）：%w", err)
	}
	owned, err := os.Lstat(target)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		removeCloneReservation(target, owned)
		return nil, err
	}
	defer removeCloneReservation(target, owned)
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(owned, opened) {
		return nil, errors.New("克隆目标已被外部替换，已保留外部内容")
	}

	// Root 将清理和发布固定在原目录；目标被移动或替换后不会递归删除新目标。
	stage := ".xsource-clone-" + rand.Text()
	if err := root.Mkdir(stage, 0700); err != nil {
		return nil, err
	}
	staging, err := root.OpenRoot(stage)
	if err != nil {
		_ = root.Remove(stage)
		return nil, err
	}
	staged, err := staging.Stat(".")
	if err != nil {
		_ = staging.Close()
		return nil, err
	}
	// 清理内容后先关闭暂存 Root，再移除空目录；Windows 会锁定打开的目录句柄。
	defer func() {
		// 只按身份移除空暂存目录，不能沿目标路径执行递归删除。
		if current, err := root.Lstat(stage); err == nil && os.SameFile(staged, current) {
			_ = root.Remove(stage)
		}
	}()
	defer staging.Close()
	defer func() {
		directory, err := staging.Open(".")
		if err != nil {
			return
		}
		entries, readErr := directory.ReadDir(-1)
		_ = directory.Close()
		if readErr != nil {
			return
		}
		for _, entry := range entries {
			_ = staging.RemoveAll(entry.Name())
		}
	}()

	stagePath := filepath.Join(target, stage)
	stageRepo := &Repository{Path: stagePath}
	if err := stageRepo.runRemote(ctx, "clone", "--", source, "."); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !sameCloneDirectory(target, owned) {
		return nil, errors.New("克隆目标已被外部替换，已保留外部内容")
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, err := directory.ReadDir(-1)
	_ = directory.Close()
	if err != nil {
		return nil, err
	}
	if len(entries) != 1 || entries[0].Name() != stage {
		return nil, errors.New("克隆目标已被外部修改，已保留外部内容")
	}
	directory, err = staging.Open(".")
	if err != nil {
		return nil, err
	}
	entries, err = directory.ReadDir(-1)
	_ = directory.Close()
	if err != nil {
		return nil, err
	}
	// 保留独占预留目录，避免移除预留再 Rename 的竞争窗口。
	// 发布包含多次重命名；若外部干预使发布失败，保留已发布内容供用户恢复。
	for _, entry := range entries {
		if _, err := root.Lstat(entry.Name()); !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("克隆目标发生冲突，已保留目录 %s", target)
		}
		if err := root.Rename(filepath.Join(stage, entry.Name()), entry.Name()); err != nil {
			return nil, fmt.Errorf("无法完成克隆，已保留目录 %s：%w", target, err)
		}
	}
	if !sameCloneDirectory(target, owned) {
		return nil, errors.New("克隆目标已被外部替换，已保留外部内容")
	}
	repo, err := Open(ctx, target)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return repo, err
}

func validateClone(source, parent, name string) error {
	if strings.TrimSpace(source) == "" || strings.HasPrefix(strings.TrimSpace(source), "-") ||
		strings.ContainsFunc(source, unicode.IsControl) {
		return errors.New("请输入有效的仓库地址，地址不能以选项开头或包含控制字符")
	}
	if remote, err := url.Parse(source); err == nil &&
		(strings.EqualFold(remote.Scheme, "http") || strings.EqualFold(remote.Scheme, "https")) && remote.User != nil {
		return errors.New("HTTPS 仓库地址不能包含账号或密码，请使用 Git 凭据管理器")
	}
	if strings.TrimSpace(parent) == "" {
		return errors.New("请选择已存在的克隆父目录")
	}
	return validateCloneDirectoryName(name)
}

func validateCloneDirectoryName(name string) error {
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, "-") ||
		strings.TrimSpace(name) != name || strings.HasSuffix(name, ".") ||
		strings.ContainsAny(name, `/\<>:"|?*`) || strings.ContainsFunc(name, unicode.IsControl) {
		return errors.New("请输入有效的单层仓库目录名")
	}
	device, _, _ := strings.Cut(strings.ToUpper(name), ".")
	device = strings.TrimRight(device, " ")
	deviceRunes := []rune(device)
	if device == "CON" || device == "PRN" || device == "AUX" || device == "NUL" ||
		device == "CONIN$" || device == "CONOUT$" ||
		(len(deviceRunes) == 4 && (strings.HasPrefix(device, "COM") || strings.HasPrefix(device, "LPT")) &&
			strings.ContainsRune("123456789¹²³", deviceRunes[3])) {
		return errors.New("仓库目录名不能使用系统保留名称")
	}
	return nil
}

func sameCloneDirectory(path string, owned os.FileInfo) bool {
	current, err := os.Lstat(path)
	return err == nil && current.IsDir() && os.SameFile(owned, current)
}

func removeCloneReservation(path string, owned os.FileInfo) {
	if sameCloneDirectory(path, owned) {
		// Remove 只移除空目录，外部添加的文件也会阻止清理。
		_ = os.Remove(path)
	}
}
