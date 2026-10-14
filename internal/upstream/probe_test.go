package upstream

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// probeQRe 拆解生成题：「<a> <op> <b>[ <op2> <c>] 等于几？直接回答。」
var probeQRe = regexp.MustCompile(`^(\d+) ([+\-×]) (\d+)(?: ([+\-]) (\d+))? 等于几？直接回答。$`)

// probeAnswer 按题面算答案；形状不合法返回 (0, false)。
func probeAnswer(q string) (int, bool) {
	m := probeQRe.FindStringSubmatch(q)
	if m == nil {
		return 0, false
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[3])
	var got int
	switch m[2] {
	case "+":
		got = a + b
		if m[5] != "" {
			c, _ := strconv.Atoi(m[5])
			if m[4] == "+" {
				got += c
			} else {
				got -= c
			}
		}
	case "-":
		got = a - b
	case "×":
		got = a * b
	}
	return got, true
}

// TestProbeQuestionShape 断言每题形状合法、答案非负、统一后缀。
func TestProbeQuestionShape(t *testing.T) {
	for i := 0; i < 3000; i++ {
		q := ProbeQuestion()
		got, ok := probeAnswer(q)
		if !ok {
			t.Fatalf("题面不合形状: %q", q)
		}
		if got < 0 {
			t.Fatalf("答案为负: %q → %d", q, got)
		}
		if !strings.HasSuffix(q, probeQuestionSuffix) {
			t.Fatalf("缺少统一后缀: %q", q)
		}
	}
}

// TestProbeQuestionVaries 断言不是固定题：200 次采样唯一题面数远大于 1，
// 且四种形状（加 / 减 / 乘 / 混合）都出现过——否则「随机」退化成新的固定指纹。
func TestProbeQuestionVaries(t *testing.T) {
	const n = 200
	seen := make(map[string]struct{}, n)
	ops := map[string]int{}
	for i := 0; i < n; i++ {
		q := ProbeQuestion()
		seen[q] = struct{}{}
		m := probeQRe.FindStringSubmatch(q)
		if m == nil {
			t.Fatalf("题面不合形状: %q", q)
		}
		if m[5] != "" {
			ops["mix"]++
		} else {
			ops[m[2]]++
		}
	}
	if len(seen) < n/2 {
		t.Errorf("唯一题面数=%d，%d 次采样下明显偏少（疑似固定题）", len(seen), n)
	}
	for _, k := range []string{"+", "-", "×", "mix"} {
		if ops[k] == 0 {
			t.Errorf("形状 %q 未出现（分布=%v）", k, ops)
		}
	}
}

// TestProbeQuestionNotLegacy 断言原硬编码题面（1+1）不会再被生成。
func TestProbeQuestionNotLegacy(t *testing.T) {
	const legacy = "1+1等于几？直接回答。"
	for i := 0; i < 500; i++ {
		if ProbeQuestion() == legacy {
			t.Fatalf("仍生成原硬编码题面: %q", legacy)
		}
	}
}

// probeRecorder 记录出站 chat 请求体里的 user 消息内容。
type probeRecorder struct {
	mu       sync.Mutex
	contents []string
}

func (r *probeRecorder) add(body []byte) {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return
	}
	for _, m := range req.Messages {
		if m.Role == "user" {
			r.mu.Lock()
			r.contents = append(r.contents, m.Content)
			r.mu.Unlock()
		}
	}
}

func (r *probeRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.contents...)
}

// TestProbeQuestionUsedByBothChatPaths 端到端断言两条探活路径都带随机算术题：
//   - 夜猫子 RunNightChats（3 轮）→ glm-5.2 chat 请求体；
//   - 桌面 DesktopChatWithExpert → fast-model chat 请求体。
//
// 两条链共 4 次请求，逐条校验题面形状，并要求题面不全相同（随机生效）。
func TestProbeQuestionUsedByBothChatPaths(t *testing.T) {
	rec := &probeRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/v2/chat/completions"):
			rec.add(raw)
			w.Header().Set("Content-Type", "text/event-stream")
			// DesktopChatWithExpert 需从 SSE 抓服务端 requestId（32hex）。
			io.WriteString(w, "data: {\"id\":\"0123456789abcdef0123456789abcdef\"}\n\n")
			io.WriteString(w, "data: [DONE]\n\n")
		case strings.HasSuffix(r.URL.Path, "/v2/report"):
			io.WriteString(w, `{"code":0,"msg":"OK"}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := &Client{
		HTTP:          srv.Client(),
		ChatHTTP:      srv.Client(),
		ChatBaseCN:    srv.URL,
		BillingBaseCN: srv.URL,
	}
	a := &auth.Auth{AccessToken: "at", UID: "u-probe", RefreshToken: "rt", ExpiresAt: 9999999999}

	// 缩短夜猫子轮间隔（生产 4s），让测试秒级完成。
	oldGap := nightChatGap
	nightChatGap = 0
	defer func() { nightChatGap = oldGap }()

	if ok, err := c.RunNightChats(a, 3); err != nil || ok != 3 {
		t.Fatalf("RunNightChats ok=%d err=%v", ok, err)
	}
	if _, _, err := c.DesktopChatWithExpert(a, ""); err != nil {
		t.Fatalf("DesktopChatWithExpert: %v", err)
	}

	got := rec.snapshot()
	if len(got) != 4 {
		t.Fatalf("user 消息数=%d want 4（3 夜猫子 + 1 桌面）；got=%q", len(got), got)
	}
	for i, q := range got {
		ans, ok := probeAnswer(q)
		if !ok {
			t.Fatalf("第 %d 条不是随机算术题: %q", i+1, q)
		}
		if ans < 0 {
			t.Fatalf("第 %d 条答案为负: %q → %d", i+1, q, ans)
		}
	}
	uniq := map[string]struct{}{}
	for _, q := range got {
		uniq[q] = struct{}{}
	}
	if len(uniq) < 2 {
		t.Errorf("4 条请求题面全同（随机未生效）: %q", got)
	}
}

// TestNightChatGapIsInjectable 断言夜猫子轮间隔可被测试替换且生产默认 4s。
func TestNightChatGapIsInjectable(t *testing.T) {
	if nightChatGap != 4*time.Second {
		t.Errorf("nightChatGap=%v want 4s（生产默认）", nightChatGap)
	}
}
