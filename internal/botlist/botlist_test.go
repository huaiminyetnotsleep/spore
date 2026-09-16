package botlist

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/config"
)

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

const (
	tokenA = "1111111111:ABCDEFGHIJKLMNOPqrstuv"
	tokenB = "2222222222:ABCDEFGHIJKLMNOPqrstuv"
	tokenC = "3333333333:ABCDEFGHIJKLMNOPqrstuv"
)

func TestResolveMergesEnvFirst(t *testing.T) {
	m := NewManager(t.TempDir(), testLog())
	m.file = FileConfig{Tokens: []string{tokenB, tokenC}}
	bots := Resolve([]string{tokenA, tokenB}, m, testLog())
	if len(bots) != 3 {
		t.Fatalf("env ∪ 文件去重后应为 3 个，得到 %d", len(bots))
	}
	// env 优先占位：tokenB 已在 env 中，不得重复出现
	if bots[0].Token != tokenA || bots[0].Source != SourceEnv {
		t.Errorf("首项应为主 bot（env），得到 %+v", bots[0])
	}
	if bots[1].Token != tokenB || bots[1].Source != SourceEnv {
		t.Errorf("重复 token 保持 env 来源，得到 %+v", bots[1])
	}
	if bots[2].Token != tokenC || bots[2].Source != SourceFile {
		t.Errorf("文件条目来源应为 file，得到 %+v", bots[2])
	}
}

func TestResolveCapsAtMaxBots(t *testing.T) {
	m := NewManager(t.TempDir(), testLog())
	tokens := make([]string, 0, config.MaxBots+5)
	for i := 0; i < config.MaxBots+5; i++ {
		tokens = append(tokens, tokenFor(i+1))
	}
	bots := Resolve(tokens, m, testLog())
	if len(bots) != config.MaxBots {
		t.Fatalf("超出上限应截断为 %d，得到 %d", config.MaxBots, len(bots))
	}
}

func tokenFor(id int) string {
	return fmt.Sprintf("%d:ABCDEFGHIJKLMNOPqrstuv", id)
}

func TestManagerAddRemoveRoundtrip(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, testLog())
	if err := m.Add(tokenB); err != nil {
		t.Fatalf("Add 失败：%v", err)
	}
	if got := m.List(); len(got) != 1 || got[0] != tokenB {
		t.Fatalf("List 应为 [tokenB]，得到 %v", got)
	}
	// 重启语义：新 Manager 从文件读回
	m2 := NewManager(dir, testLog())
	if got := m2.List(); len(got) != 1 || got[0] != tokenB {
		t.Fatalf("重载后应为 [tokenB]，得到 %v", got)
	}
	if err := m2.Add(tokenB); err == nil {
		t.Fatal("重复 Add 应报错")
	}
	if err := m2.Remove(tokenB); err != nil {
		t.Fatalf("Remove 失败：%v", err)
	}
	if got := m2.List(); len(got) != 0 {
		t.Fatalf("移除后应为空，得到 %v", got)
	}
	if err := m2.Remove(tokenB); err == nil {
		t.Fatal("移除不存在的条目应报错")
	}
}

func TestManagerRejectsInvalidToken(t *testing.T) {
	m := NewManager(t.TempDir(), testLog())
	if err := m.Add("not-a-token"); err == nil {
		t.Fatal("非法 token 应被拒绝")
	}
	if got := m.List(); len(got) != 0 {
		t.Fatalf("非法 token 不得落盘，得到 %v", got)
	}
}

func TestLoadFileCorruptIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatalf("写损坏文件失败：%v", err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("损坏文件应返回错误")
	}
	m := NewManager(dir, testLog())
	if m.LoadError() == nil {
		t.Fatal("NewManager 应保留加载错误供事件上报")
	}
	if got := m.List(); len(got) != 0 {
		t.Fatalf("损坏文件条目应被忽略，得到 %v", got)
	}
}

func TestSessionPathIsolation(t *testing.T) {
	dir := "data"
	if got, want := SessionPath(dir, tokenA, true), filepath.Join(dir, "bot-session.json"); got != want {
		t.Errorf("主 bot 会话应沿用历史文件 %q，得到 %q", want, got)
	}
	if got, want := SessionPath(dir, tokenB, false), filepath.Join(dir, "bot-session-2222222222.json"); got != want {
		t.Errorf("非主 bot 会话应按 bot id 隔离 %q，得到 %q", want, got)
	}
	if BotID(tokenB) != 2222222222 {
		t.Errorf("BotID 应为 token 数字前缀，得到 %d", BotID(tokenB))
	}
}
