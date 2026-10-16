package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClassifyLine(t *testing.T) {
	cases := map[string]string{
		"| #001 | glm-5.2 | stream | 200 | uid=c8a3e793 | TTFB=120ms |": ChChat,
		"school c8a3e793: ★ 分享任务完成":                                     ChTask,
		"streak-bonus 5c162cc9: 🎊 新手礼包 +100c":                           ChTask,
		"blackcat c8a3e793: 完成 3 次夜间对话":                                 ChTask,
		"checkin 5c162cc9: 已签到":                                         ChTask,
		"panel: 任务动作 uid=x code=chat_5":                                 ChTask,
		"panel: 队列启动：6 项（并发 2）":                                         ChTask,
		"panel: revive uid=x":                                           ChSys,
		"workbuddy2api listening on :7863":                              ChSys,
		"scheduler: 余额后台刷新每 5m0s":                                       ChSys,
	}
	for line, want := range cases {
		if got := classifyLine(line); got != want {
			t.Errorf("classifyLine(%q)=%q want %q", line, got, want)
		}
	}
}

func TestRingWriteStripsTimestamp(t *testing.T) {
	r := NewRing(4)
	if _, err := r.Write([]byte("2026/09/14 00:12:34 school x: done\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Write([]byte("| #002 | glm | stream | 200 | ok |")); err != nil {
		t.Fatal(err)
	}
	es := r.Snapshot()
	if len(es) != 2 {
		t.Fatalf("entries=%d want 2", len(es))
	}
	if strings.HasPrefix(es[0].Text, "2026/") {
		t.Errorf("timestamp not stripped: %q", es[0].Text)
	}
	if es[0].Ch != ChTask || es[1].Ch != ChChat {
		t.Errorf("channels: %q %q", es[0].Ch, es[1].Ch)
	}
	if time.Since(es[0].TS) > 5*time.Second {
		t.Errorf("stale ts: %v", es[0].TS)
	}
}

// TestLogsAPINewestFirst /panel/api/logs 必须**按时间倒序**返回（最新在前），
// 与「请求记录」口径一致（reqlog.ReadArchive 也是倒序）——面板两个视图都只按
// 接收顺序渲染，不各自排序，所以顺序由 API 保证。
//
// 注意与 Ring.Snapshot 的契约区分：Snapshot 恒按写入顺序（升序，见上一条用例），
// 倒序在 handler 边界做。
func TestLogsAPINewestFirst(t *testing.T) {
	p := New(Config{Version: "test", APIKey: "test-key"})
	r := p.Logs()
	for _, line := range []string{"第一行", "第二行", "第三行"} {
		if _, err := r.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}

	// Ring 契约不变：Snapshot 仍是写入顺序。
	snap := r.Snapshot()
	if len(snap) != 3 || snap[0].Text != "第一行" {
		t.Fatalf("Snapshot 应保持写入顺序, got %+v", snap)
	}

	req := httptest.NewRequest("GET", "/panel/api/logs", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Entries []LogEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("resp not json: %v", err)
	}
	want := []string{"第三行", "第二行", "第一行"}
	if len(got.Entries) != len(want) {
		t.Fatalf("entries=%d want %d", len(got.Entries), len(want))
	}
	for i, w := range want {
		if got.Entries[i].Text != w {
			t.Errorf("entries[%d]=%q want %q（API 须倒序：最新在前）", i, got.Entries[i].Text, w)
		}
	}
}

// TestPanelReusesProvidedLogRing 断言 panel.New 复用 Config.LogRing：main 在启动
// 最早期就建环并把 log/chat 镜像装上（覆盖「配置生成 / 账号加载 / 排程启用」这些
// 早于面板构造的日志），面板必须读到**同一个**环实例。
//
// 若各建各的环：启动期日志写进 main 的环、面板读自己新建的环，视图里永远看不到
// 那些行（修复前的实际表现：out.log 有 12 行启动日志，API 只返回 2 条）。
func TestPanelReusesProvidedLogRing(t *testing.T) {
	ring := NewRing(10)
	// 模拟「早于 panel.New 的启动期日志」。
	if _, err := ring.Write([]byte("loaded 2 account(s) from ./auths\n")); err != nil {
		t.Fatal(err)
	}
	p := New(Config{Version: "test", APIKey: "test-key", LogRing: ring})
	if p.Logs() != ring {
		t.Fatal("panel.New 应复用 Config.LogRing（同一实例）")
	}
	req := httptest.NewRequest("GET", "/panel/api/logs", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "loaded 2 account(s)") {
		t.Errorf("启动期日志未出现在 /panel/api/logs: %s", rec.Body.String())
	}
	// 未提供 LogRing 时自建（既有行为不变，测试/嵌入式用法零改动）。
	if p2 := New(Config{Version: "test", APIKey: "k"}); p2.Logs() == nil {
		t.Error("未提供 LogRing 时应自建")
	}
}
