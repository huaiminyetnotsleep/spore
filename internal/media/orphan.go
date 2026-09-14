package media

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// errConsumerClosed 标记消费方主动放弃（非下载失败），用于关闭流式管道读端。
var errConsumerClosed = errors.New("consumer closed")

// TempName 生成任务临时文件名：<jobID>-<文件名>。
// 命名契约的唯一来源：落盘（handle.go）与孤儿清理（IsTempName）都以此为准。
// 注意 jobID 必须是纯数字（queue.NewJob 产出的纳秒时间戳满足此约束）。
func TempName(jobID, fileName string) string {
	return fmt.Sprintf("%s-%s", jobID, filepath.Base(fileName))
}

// IsTempName 判断名字是否按 TempName 规则生成（首段为纯数字）。
// 启动期孤儿清理只删除匹配项，TEMP_DIR 误配到已有目录时不会殃及无关文件。
func IsTempName(name string) bool {
	i := -1
	for j, c := range name {
		if c == '-' {
			i = j
			break
		}
	}
	if i <= 0 {
		return false
	}
	for _, c := range name[:i] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// CleanOrphans 清扫目录中本程序遗留的任务临时文件；目录不存在等情况静默跳过。
// 供启动期调用（main），删除逻辑与命名逻辑同源，避免隐式协议漂移。
func CleanOrphans(dir string, logger *slog.Logger) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !IsTempName(name) {
			continue
		}
		if rmErr := os.RemoveAll(filepath.Join(dir, name)); rmErr != nil {
			logger.Warn("清理孤儿临时文件失败", "name", name, "error", rmErr.Error())
		}
	}
}
