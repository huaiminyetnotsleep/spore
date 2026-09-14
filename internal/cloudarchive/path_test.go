package cloudarchive

import (
	"strings"
	"testing"
	"time"
)

func planDate() time.Time { return time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC) }

func pathsOf(p Plan) []string {
	out := make([]string, 0, len(p.Files))
	for _, f := range p.Files {
		out = append(out, f.RemotePath)
	}
	return out
}

func TestBuildPlan(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		in     PlanInput
		want   []string
	}{
		{
			name:   "单媒体无配文平铺",
			prefix: "spore",
			in: PlanInput{ChannelName: "example", Date: planDate(), MessageID: 7,
				Media: []PlanMedia{{FileName: "clip.mp4"}}},
			want: []string{"spore/example/2026-09-09/clip.mp4"},
		},
		{
			name:   "单媒体带配文成夹",
			prefix: "spore",
			in: PlanInput{ChannelName: "example", Date: planDate(), MessageID: 7,
				Media:   []PlanMedia{{FileName: "clip.mp4"}},
				Caption: "说明文字"},
			want: []string{"spore/example/2026-09-09/7/01_clip.mp4",
				"spore/example/2026-09-09/7/caption.txt"},
		},
		{
			name:   "相册多媒体序号前缀与配文",
			prefix: "spore",
			in: PlanInput{ChannelName: "example", Date: planDate(), MessageID: 42,
				Media:   []PlanMedia{{FileName: "a.jpg"}, {FileName: "b.mp4"}, {FileName: "c.pdf"}},
				Caption: "相册配文"},
			want: []string{"spore/example/2026-09-09/42/01_a.jpg",
				"spore/example/2026-09-09/42/02_b.mp4",
				"spore/example/2026-09-09/42/03_c.pdf",
				"spore/example/2026-09-09/42/caption.txt"},
		},
		{
			// 01_/02_ 序号前缀天然区分组内同名；-2/-3 兜底仅在生成名
			// 真正撞车时触发（见 TestDedupeName）
			name:   "相册内同名经序号前缀区分",
			prefix: "p",
			in: PlanInput{ChannelName: "ex", Date: planDate(), MessageID: 9,
				Media: []PlanMedia{{FileName: "a.jpg"}, {FileName: "a.jpg"}, {FileName: "a-2.jpg"}}},
			want: []string{"p/ex/2026-09-09/9/01_a.jpg",
				"p/ex/2026-09-09/9/02_a.jpg",
				"p/ex/2026-09-09/9/03_a-2.jpg"},
		},
		{
			name:   "photo 无名按 msgid 命名",
			prefix: "spore",
			in: PlanInput{ChannelName: "example", Date: planDate(), MessageID: 100,
				Media: []PlanMedia{{IsPhoto: true}, {IsPhoto: true}}},
			want: []string{"spore/example/2026-09-09/100/01_photo-100-1.jpg",
				"spore/example/2026-09-09/100/02_photo-100-2.jpg"},
		},
		{
			name:   "单 photo 平铺命名",
			prefix: "spore",
			in: PlanInput{ChannelName: "example", Date: planDate(), MessageID: 33,
				Media: []PlanMedia{{IsPhoto: true}}},
			want: []string{"spore/example/2026-09-09/photo-33-1.jpg"},
		},
		{
			name:   "无前缀与私有频道键",
			prefix: "",
			in: PlanInput{ChannelName: "-1001234567890", Date: planDate(), MessageID: 5,
				Media: []PlanMedia{{FileName: "f.bin"}}},
			want: []string{"-1001234567890/2026-09-09/f.bin"},
		},
		{
			name:   "文件名剔除路径分隔符与控制字符",
			prefix: "p",
			in: PlanInput{ChannelName: "ex", Date: planDate(), MessageID: 3,
				Media: []PlanMedia{{FileName: "a/b\\c\x07d.mp4"}}},
			want: []string{"p/ex/2026-09-09/abcd.mp4"},
		},
		{
			name:   "文件名全非法回落",
			prefix: "p",
			in: PlanInput{ChannelName: "ex", Date: planDate(), MessageID: 3,
				Media: []PlanMedia{{FileName: "///"}}},
			want: []string{"p/ex/2026-09-09/file"},
		},
		{
			name:   "超长文件名截断保留扩展名",
			prefix: "p",
			in: PlanInput{ChannelName: "ex", Date: planDate(), MessageID: 3,
				Media: []PlanMedia{{FileName: strings.Repeat("长", 200) + ".mp4"}}},
			want: []string{"p/ex/2026-09-09/" + strings.Repeat("长", 116) + ".mp4"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pathsOf(BuildPlan(tt.prefix, tt.in))
			if len(got) != len(tt.want) {
				t.Fatalf("布局不符:\nwant %v\ngot  %v", tt.want, got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("第 %d 个路径不符:\nwant %q\ngot  %q", i, tt.want[i], got[i])
				}
			}
		})
	}
}

// MediaIndex 与 IsCaption 的映射正确性（worker 据此打开媒体与写配文）。
func TestBuildPlanIndexing(t *testing.T) {
	p := BuildPlan("p", PlanInput{ChannelName: "ex", Date: planDate(), MessageID: 1,
		Media:   []PlanMedia{{FileName: "a"}, {FileName: "b"}},
		Caption: "cap"})
	if !p.Grouped {
		t.Fatal("相册应成夹")
	}
	wantDir := "p/ex/2026-09-09/1"
	if p.Dir != wantDir {
		t.Fatalf("目录不符: %q", p.Dir)
	}
	for i, f := range p.Files {
		if f.IsCaption {
			if f.MediaIndex != -1 || f.FileName != "caption.txt" {
				t.Errorf("caption 文件元数据不符: %+v", f)
			}
			continue
		}
		if f.MediaIndex != i {
			t.Errorf("文件 %d 的 MediaIndex 应为 %d: %+v", i, i, f)
		}
	}
}

// 截断边界：截断后恰好落在多字节字符内也必须保持 UTF-8 合法。
func TestSanitizeSegmentUTF8(t *testing.T) {
	out := sanitizeSegment(strings.Repeat("α", 300)+string(rune(0x2028))+"/x", "fb")
	if !isUTF8Valid(out) {
		t.Fatalf("截断结果应为合法 UTF-8: %q", out)
	}
	if strings.ContainsAny(out, "/\\\x00") {
		t.Fatalf("应剔除分隔符: %q", out)
	}
}

// dedupeName 的 -2/-3 兜底语义（含生成名与既有名撞车的二次避让）。
func TestDedupeName(t *testing.T) {
	used := map[string]int{}
	if got := dedupeName("a.txt", used); got != "a.txt" {
		t.Fatalf("首次应原样: %q", got)
	}
	if got := dedupeName("a.txt", used); got != "a-2.txt" {
		t.Fatalf("第二次应 -2: %q", got)
	}
	if got := dedupeName("a-2.txt", used); got != "a-2-2.txt" {
		t.Fatalf("撞既有 a-2.txt 应再避让: %q", got)
	}
	if got := dedupeName("a.txt", used); got != "a-3.txt" {
		t.Fatalf("第三次应 -3: %q", got)
	}
	if got := dedupeName("noext", used); got != "noext" {
		t.Fatalf("无扩展名首见应原样: %q", got)
	}
	if got := dedupeName("noext", used); got != "noext-2" {
		t.Fatalf("无扩展名冲突应追加后缀: %q", got)
	}
}

func isUTF8Valid(s string) bool {
	return len(s) == 0 || strings.ToValidUTF8(s, "") == s
}
