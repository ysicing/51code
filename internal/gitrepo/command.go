// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package gitrepo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const outputLimit = 4 << 20

var errOutputTooLarge = errors.New("Git 输出超过 4 MiB，请使用命令行查看完整结果")

type limitedBuffer struct {
	// 不嵌入 bytes.Buffer，避免 io.Copy 通过提升的 ReadFrom 绕过 Write 限制。
	content  bytes.Buffer
	overflow bool
	// limit 为 0 时使用 outputLimit，零值可直接用于 stderr 等场景。
	limit int
	// 只有只读预览可以在溢出时取消；通用命令需要等待写操作和钩子收尾。
	cancel context.CancelFunc
}

func (buffer *limitedBuffer) String() string {
	return buffer.content.String()
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	size := len(data)
	limit := buffer.limit
	if limit <= 0 {
		limit = outputLimit
	}
	remaining := limit - buffer.content.Len()
	if size > remaining {
		buffer.overflow = true
		data = data[:remaining]
		if buffer.cancel != nil {
			buffer.cancel()
		}
	}
	_, _ = buffer.content.Write(data)
	return size, nil
}

type commandError struct {
	code    int
	message string
}

func (err *commandError) Error() string { return err.message }

func exitCode(err error, code int) bool {
	var failure *commandError
	return errors.As(err, &failure) && failure.code == code
}

func gitEnvironment() []string {
	local := map[string]bool{
		"GIT_EXEC_PATH":                    true,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_CONFIG": true,
		"GIT_CONFIG_PARAMETERS": true, "GIT_CONFIG_COUNT": true,
		"GIT_OBJECT_DIRECTORY": true, "GIT_DIR": true, "GIT_WORK_TREE": true,
		"GIT_IMPLICIT_WORK_TREE": true, "GIT_GRAFT_FILE": true,
		"GIT_INDEX_FILE": true, "GIT_NO_REPLACE_OBJECTS": true,
		"GIT_REPLACE_REF_BASE": true, "GIT_PREFIX": true,
		"GIT_SHALLOW_FILE": true, "GIT_COMMON_DIR": true,
	}
	environment := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		if !local[key] && !strings.HasPrefix(key, "GIT_CONFIG_KEY_") && !strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
			environment = append(environment, entry)
		}
	}
	// 只读刷新（status、diff）默认会顺带回写索引并抢占 index.lock，
	// 与用户在终端中的提交、暂存冲突；必要的写操作不受此变量影响。
	return append(environment, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_PAGER=cat",
		"GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true", "GIT_MERGE_AUTOEDIT=no", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
}

func run(ctx context.Context, root, input string, timeout time.Duration, args ...string) (string, error) {
	var stdout limitedBuffer
	if err := runTo(ctx, root, input, timeout, &stdout, stdout.String, args...); err != nil {
		return "", err
	}
	if stdout.overflow {
		return "", errOutputTooLarge
	}
	return stdout.String(), nil
}

// runCapped 最多保留 limit 字节标准输出；超出时立即结束子进程，返回已读部分且 truncated 为 true、
// err 为 nil，仅供只读预览截断展示。调用方 Context 取消或超时仍按失败返回。
func runCapped(ctx context.Context, root, input string, timeout time.Duration, limit int, args ...string) (string, bool, error) {
	processCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := limitedBuffer{limit: limit, cancel: cancel}
	err := runTo(processCtx, root, input, timeout, &stdout, func() string { return stdout.String() }, args...)
	// 溢出后由我们主动取消，此时 runTo 的取消错误不是真正的失败。
	if stdout.overflow && ctx.Err() == nil {
		return stdout.String(), true, nil
	}
	if err != nil {
		return "", false, err
	}
	return stdout.String(), false, nil
}

// trimPartialLine 去掉截断输出末尾不完整的一行，避免预览出现半行或被切断的多字节字符。
func trimPartialLine(output string) string {
	if index := strings.LastIndexByte(output, '\n'); index >= 0 {
		return output[:index+1]
	}
	return ""
}

// runDigest 将完整输出流式计入 SHA-256，用于大仓库索引等只需判断是否变化、
// 不需要保留内容且可能超过输出上限的场景。
func runDigest(ctx context.Context, root string, timeout time.Duration, args ...string) (string, error) {
	digest := sha256.New()
	if err := runTo(ctx, root, "", timeout, digest, func() string { return "" }, args...); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func runTo(ctx context.Context, root, input string, timeout time.Duration, stdout io.Writer, details func() string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	options := []string{"--no-pager", "--literal-pathspecs", "-C", root, "-c", "color.ui=false"}
	cmd, err := gitCommand(ctx, append(options, args...)...)
	if err != nil {
		return err
	}
	cmd.Stdin = strings.NewReader(input)
	cmd.WaitDelay = 2 * time.Second
	var stderr limitedBuffer
	cmd.Stdout, cmd.Stderr = stdout, &stderr
	err = runProcess(ctx, cmd)
	if ctx.Err() != nil {
		return fmt.Errorf("Git 操作已取消或超时：%w", ctx.Err())
	}
	if err != nil {
		code := -1
		var failure *exec.ExitError
		if errors.As(err, &failure) {
			code = failure.ExitCode()
		}
		message := strings.TrimSpace(stderr.String())
		if details := strings.TrimSpace(details()); details != "" {
			message = strings.TrimSpace(details + "\n" + message)
		}
		if message == "" {
			message = err.Error()
		}
		return &commandError{code: code, message: message}
	}
	if stderr.overflow {
		return errOutputTooLarge
	}
	return nil
}

// nulRecordWriter 把 NUL 结尾的记录逐条交给 record，使大输出不必整段读入内存，
// 也不受 outputLimit 限制；单条记录仍限制在 outputLimit 内。record 返回错误时取消子进程。
type nulRecordWriter struct {
	record  func(string) error
	cancel  context.CancelFunc
	partial []byte
	err     error
}

func (writer *nulRecordWriter) Write(data []byte) (int, error) {
	if writer.err != nil {
		return 0, writer.err
	}
	size := len(data)
	for len(data) > 0 {
		end := bytes.IndexByte(data, 0)
		if end < 0 {
			if len(writer.partial)+len(data) > outputLimit {
				return 0, writer.fail(errOutputTooLarge)
			}
			writer.partial = append(writer.partial, data...)
			break
		}
		if len(writer.partial)+end > outputLimit {
			return 0, writer.fail(errOutputTooLarge)
		}
		record := string(append(writer.partial, data[:end]...))
		writer.partial = writer.partial[:0]
		data = data[end+1:]
		if err := writer.record(record); err != nil {
			return 0, writer.fail(err)
		}
	}
	return size, nil
}

func (writer *nulRecordWriter) fail(err error) error {
	writer.err = err
	if writer.cancel != nil {
		writer.cancel()
	}
	return err
}

// flush 处理末尾缺少 NUL 的最后一条记录，与按 NUL 拆分整段输出的结果一致。
func (writer *nulRecordWriter) flush() error {
	if writer.err != nil || len(writer.partial) == 0 {
		return writer.err
	}
	record := string(writer.partial)
	writer.partial = nil
	return writer.record(record)
}

// runRecords 流式读取 NUL 分隔的输出。
func runRecords(ctx context.Context, root string, timeout time.Duration, record func(string) error, args ...string) error {
	processCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	writer := nulRecordWriter{record: record, cancel: cancel}
	err := runTo(processCtx, root, "", timeout, &writer, func() string { return "" }, args...)
	if writer.err != nil && ctx.Err() == nil {
		return writer.err
	}
	if err != nil {
		return err
	}
	return writer.flush()
}
