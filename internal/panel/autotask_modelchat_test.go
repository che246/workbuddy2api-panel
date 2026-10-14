package panel

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// modelChatQRe 与 upstream 侧同款题面形状（「<a> <op> <b>[ <op2> <c>] 等于几？直接回答。」）。
var modelChatQRe = regexp.MustCompile(`^\d+ [+\-×] \d+(?: [+\-] \d+)? 等于几？直接回答。$`)

// TestRunModelChatUsesProbeQuestion 断言 runModelChat 的 glm-5.2 真实对话
// 用的是 upstream.ProbeQuestion() 随机算术题，而非原硬编码 "hi，请回复一句话"：
//   - 出站 chat body 的 user 消息匹配题面形状；
//   - 不再出现原硬编码文案；
//   - 上报的 requestModelId/requestModelName 仍对齐 glm-5.2（判据字段不能改坏）。
func TestRunModelChatUsesProbeQuestion(t *testing.T) {
	var chatUserMsgs []string
	var reportModelID, reportModelName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/v2/chat/completions"):
			var req struct {
				Model    string `json:"model"`
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Errorf("chat body 非法 JSON: %v", err)
			}
			if req.Model != "glm-5.2" {
				t.Errorf("chat model=%q want glm-5.2", req.Model)
			}
			for _, m := range req.Messages {
				if m.Role == "user" {
					chatUserMsgs = append(chatUserMsgs, m.Content)
				}
			}
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: [DONE]\n\n")
		case strings.HasSuffix(r.URL.Path, "/v2/report"):
			var arr []map[string]any
			_ = json.Unmarshal(raw, &arr)
			if len(arr) > 0 {
				reportModelID, _ = arr[0]["requestModelId"].(string)
				reportModelName, _ = arr[0]["requestModelName"].(string)
			}
			io.WriteString(w, `{"code":0,"msg":"OK"}`)
		case strings.HasSuffix(r.URL.Path, "/tasks/accept"):
			io.WriteString(w, `{"code":0,"msg":"OK","data":{}}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	up := &upstream.Client{
		HTTP:          srv.Client(),
		ChatHTTP:      srv.Client(),
		ChatBaseCN:    srv.URL,
		BillingBaseCN: srv.URL,
	}
	p := New(Config{Version: "test", APIKey: "k", Upstream: up})

	// 缩短动作间隔（生产 1.05s×2），让测试秒级完成。
	oldGap := reportGap
	reportGap = 0
	defer func() { reportGap = oldGap }()

	msg, err := runModelChat(p, &auth.Auth{AccessToken: "at", UID: "u-mc", ExpiresAt: 9999999999})
	if err != nil {
		t.Fatalf("runModelChat: %v", err)
	}
	if !strings.Contains(msg, "已完成") {
		t.Errorf("返回文案异常: %q", msg)
	}

	if len(chatUserMsgs) != 1 {
		t.Fatalf("user 消息数=%d want 1；got=%q", len(chatUserMsgs), chatUserMsgs)
	}
	q := chatUserMsgs[0]
	if !modelChatQRe.MatchString(q) {
		t.Errorf("对话内容不是随机算术题: %q", q)
	}
	if q == "hi，请回复一句话" {
		t.Errorf("仍在使用原硬编码文案: %q", q)
	}

	// 判据字段回归：上报的模型必须仍对齐 glm-5.2。
	if reportModelID != "glm-5.2" || reportModelName != "GLM-5.2" {
		t.Errorf("上报模型=%q/%q want glm-5.2/GLM-5.2", reportModelID, reportModelName)
	}
}

// TestRunModelChatQuestionVariesAcrossRuns 断言同一账号连续两次跑出的题面不同
// （原硬编码下两次逐字相同，这正是要消除的重复指纹）。
func TestRunModelChatQuestionVariesAcrossRuns(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/v2/chat/completions"):
			var req struct {
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			_ = json.Unmarshal(raw, &req)
			mu.Lock()
			for _, m := range req.Messages {
				if m.Role == "user" {
					msgs = append(msgs, m.Content)
				}
			}
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: [DONE]\n\n")
		case strings.HasSuffix(r.URL.Path, "/v2/report"):
			io.WriteString(w, `{"code":0,"msg":"OK"}`)
		case strings.HasSuffix(r.URL.Path, "/tasks/accept"):
			io.WriteString(w, `{"code":0,"msg":"OK","data":{}}`)
		}
	}))
	defer srv.Close()

	up := &upstream.Client{
		HTTP: srv.Client(), ChatHTTP: srv.Client(),
		ChatBaseCN: srv.URL, BillingBaseCN: srv.URL,
	}
	p := New(Config{Version: "test", APIKey: "k", Upstream: up})
	oldGap := reportGap
	reportGap = 0
	defer func() { reportGap = oldGap }()

	a := &auth.Auth{AccessToken: "at", UID: "u-mc2", ExpiresAt: 9999999999}
	for i := 0; i < 2; i++ {
		if _, err := runModelChat(p, a); err != nil {
			t.Fatalf("第 %d 次 runModelChat: %v", i+1, err)
		}
	}
	mu.Lock()
	got := append([]string(nil), msgs...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("user 消息数=%d want 2；got=%q", len(got), got)
	}
	if got[0] == got[1] {
		t.Errorf("两次跑出的题面逐字相同（随机未生效）: %q", got[0])
	}
	for i, q := range got {
		if !modelChatQRe.MatchString(q) {
			t.Errorf("第 %d 条不是随机算术题: %q", i+1, q)
		}
	}
}

// TestReportGapInjectable 断言动作间隔可被测试替换且生产默认 1.05s。
func TestReportGapInjectable(t *testing.T) {
	if reportGap != 1050*time.Millisecond {
		t.Errorf("reportGap=%v want 1.05s（生产默认）", reportGap)
	}
}
