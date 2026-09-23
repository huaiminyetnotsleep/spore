package botapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// pin_prompt_test.go — /pin 模式选择交互与配对提交的端到端行为。

// 云盘开启：/pin 粘贴链接后进入模式选择阶段，不触发提交。
func TestPinPromptInputShowsModeSelection(t *testing.T) {
	opt, fa, fs, prompter := pinPromptHarness(t, 4)
	opt.CloudStatus = fakeCloudStatus{enabled: true, avail: true, def: "mega-1", dests: []string{"mega-1"}}
	prompter.register(101, "/pin", pinUsage, 7, 7)
	entry, ok := prompter.get(101, 7)
	if !ok {
		t.Fatal("pending should exist")
	}

	handlePinPromptInput(context.Background(), opt, fs, prompter,
		models.User{ID: 7}, 7, entry, "https://t.me/example_channel/9")

	got, ok := prompter.get(101, 7)
	if !ok || got.stage != promptAwaitPinMode || got.input != "https://t.me/example_channel/9" {
		t.Fatalf("pending after input = (%+v, %v), want promptAwaitPinMode with input", got, ok)
	}
	if n := len(fa.submitted()); n != 0 {
		t.Fatalf("mode selection must not submit, got %d", n)
	}
}

func pinPromptHarness(t *testing.T, capacity int) (Options, *fakeAccess, *fakeSender, *inputPrompter) {
	t.Helper()
	opt, fa, fs := newHarness(t, capacity)
	fa.usageData.Status = store.UserEnabled // requireEnabled 准入通过
	prompter := newInputPrompter()
	return opt, fa, fs, prompter
}

// 云盘关闭：不进模式选择，直接按仅置顶提交（历史行为）。
func TestPinPromptInputSkipsSelectionWhenCloudDisabled(t *testing.T) {
	opt, fa, fs, prompter := pinPromptHarness(t, 4)
	prompter.register(101, "/pin", pinUsage, 7, 7)
	entry, _ := prompter.get(101, 7)

	handlePinPromptInput(context.Background(), opt, fs, prompter,
		models.User{ID: 7}, 7, entry, "https://t.me/example_channel/9")

	subs := fa.submitted()
	if len(subs) != 1 || !subs[0].Pin || subs[0].CloudDest != "" {
		t.Fatalf("submissions = %+v, want single pin submission", subs)
	}
	if _, ok := prompter.get(101, 7); ok {
		t.Fatal("pending should be consumed on direct submission")
	}
}

func pinModeCallback(promptID int, withCloud bool) *models.CallbackQuery {
	mode := "0"
	if withCloud {
		mode = "1"
	}
	return &models.CallbackQuery{
		ID:   "cb-pin",
		From: models.User{ID: 7},
		Data: promptPinModeCallbackPrefix + itoa(promptID) + ":" + mode,
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{
			ID: promptID + 1, Chat: models.Chat{ID: 7, Type: models.ChatTypePrivate},
		}},
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func runPinModeCallback(t *testing.T, opt Options, fs *fakeSender, prompter *inputPrompter, withCloud bool) {
	t.Helper()
	bot := commandTestBot(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"ok":true,"result":true}`)
	})
	handlePromptCallback(context.Background(), opt, fs, bot, pinModeCallback(101, withCloud), prompter)
}

// 回调「仅置顶」：一次 TG 提交（pin 标记），不产生云盘任务。
func TestPinModeCallbackPinOnly(t *testing.T) {
	opt, fa, fs, prompter := pinPromptHarness(t, 4)
	opt.CloudStatus = fakeCloudStatus{enabled: true, avail: true, def: "mega-1", dests: []string{"mega-1"}}
	prompter.register(101, "/pin", pinUsage, 7, 7)
	if _, ok := prompter.beginPinMode(101, 7, "https://t.me/example_channel/9"); !ok {
		t.Fatal("begin pin mode failed")
	}

	runPinModeCallback(t, opt, fs, prompter, false)

	subs := fa.submitted()
	if len(subs) != 1 || !subs[0].Pin || subs[0].CloudDest != "" {
		t.Fatalf("submissions = %+v, want single pin submission", subs)
	}
}

// 回调「置顶+转存」：配对提交——先 TG（pin、不豁免间隔）后云盘（默认目的地、
// 豁免间隔）。
func TestPinModeCallbackPinWithCloud(t *testing.T) {
	opt, fa, fs, prompter := pinPromptHarness(t, 4)
	opt.CloudStatus = fakeCloudStatus{enabled: true, avail: true, def: "mega-1", dests: []string{"mega-1"}}
	prompter.register(101, "/pin", pinUsage, 7, 7)
	if _, ok := prompter.beginPinMode(101, 7, "https://t.me/example_channel/9"); !ok {
		t.Fatal("begin pin mode failed")
	}

	runPinModeCallback(t, opt, fs, prompter, true)

	subs := fa.submitted()
	if len(subs) != 2 {
		t.Fatalf("submissions = %d, want paired pair", len(subs))
	}
	if !subs[0].Pin || subs[0].CloudDest != "" || subs[0].BatchContinuation {
		t.Fatalf("first submission = %+v, want TG pin without continuation", subs[0])
	}
	if subs[1].Pin || subs[1].CloudDest != "mega-1" || !subs[1].BatchContinuation {
		t.Fatalf("second submission = %+v, want cloud submission with continuation", subs[1])
	}
}

// 云盘运行时关闭后选「置顶+转存」：降级为仅置顶。
func TestPinModeCallbackCloudUnavailableFallsBack(t *testing.T) {
	opt, fa, fs, prompter := pinPromptHarness(t, 4)
	opt.CloudStatus = fakeCloudStatus{enabled: false, avail: true, def: "mega-1", dests: []string{"mega-1"}}
	prompter.register(101, "/pin", pinUsage, 7, 7)
	if _, ok := prompter.beginPinMode(101, 7, "https://t.me/example_channel/9"); !ok {
		t.Fatal("begin pin mode failed")
	}

	runPinModeCallback(t, opt, fs, prompter, true)

	subs := fa.submitted()
	if len(subs) != 1 || !subs[0].Pin || subs[0].CloudDest != "" {
		t.Fatalf("submissions = %+v, want fallback to pin only", subs)
	}
}

// 未配置默认目的地时选「置顶+转存」：降级为仅置顶。
func TestPinModeCallbackNoDefaultDestinationFallsBack(t *testing.T) {
	opt, fa, fs, prompter := pinPromptHarness(t, 4)
	opt.CloudStatus = fakeCloudStatus{enabled: true, avail: true, def: "", dests: []string{}}
	prompter.register(101, "/pin", pinUsage, 7, 7)
	if _, ok := prompter.beginPinMode(101, 7, "https://t.me/example_channel/9"); !ok {
		t.Fatal("begin pin mode failed")
	}

	runPinModeCallback(t, opt, fs, prompter, true)

	subs := fa.submitted()
	if len(subs) != 1 || !subs[0].Pin || subs[0].CloudDest != "" {
		t.Fatalf("submissions = %+v, want fallback to pin only", subs)
	}
}

// 面板渲染：两个模式按钮 + 用法/取消控制行。
func TestRenderPinModeSelection(t *testing.T) {
	entry := pendingPrompt{stage: promptAwaitPinMode, promptMessageID: 123}
	text, rawMarkup, ok := renderPinModeSelection(entry)
	if !ok || !strings.Contains(text, "请选择提交方式") {
		t.Fatalf("render = (%q, %v)", text, ok)
	}
	markup, ok := rawMarkup.(*models.InlineKeyboardMarkup)
	if !ok {
		t.Fatalf("markup = %T", rawMarkup)
	}
	if len(markup.InlineKeyboard) != 2 {
		t.Fatalf("rows = %d, want mode row plus controls", len(markup.InlineKeyboard))
	}
	if markup.InlineKeyboard[0][0].Text != "📌 仅置顶" || markup.InlineKeyboard[0][1].Text != "📌☁️ 置顶+转存网盘" {
		t.Fatalf("mode buttons = %+v", markup.InlineKeyboard[0])
	}
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if len([]byte(button.CallbackData)) > 64 {
				t.Fatalf("unsafe callback data %q", button.CallbackData)
			}
		}
	}
}

// 配对提交：先 TG（pin）后云盘（默认目的地、豁免间隔）。
func TestSubmitPinWithCloudPairsSubmissions(t *testing.T) {
	opt, fa, fs, _ := pinPromptHarness(t, 4)
	refs := tmeurl.ParseAll("https://t.me/example_channel/9")

	submitPinWithCloud(context.Background(), opt, fs, models.User{ID: 7}, 7, refs, "mega-1")

	subs := fa.submitted()
	if len(subs) != 2 || !subs[0].Pin || subs[1].CloudDest != "mega-1" || !subs[1].BatchContinuation {
		t.Fatalf("submissions = %+v", subs)
	}
}
