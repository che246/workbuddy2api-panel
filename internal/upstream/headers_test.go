package upstream

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// uuidRe 带连字符的 UUIDv4 形态（8-4-4-4-12，版本位 4、变体位 8/9/a/b）。
var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// hex32Re 32 位无连字符 hex。
var hex32Re = regexp.MustCompile(`^[0-9a-f]{32}$`)

// newReqWithHeaders 造一个可注入头的请求（不带 body，头注入不读 body）。
func newReqWithHeaders(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://chat.example/v2/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// TestChatHeadersDropMachineSessionIDs 断言 chat 出站**不带** X-Machine-ID /
// X-Session-ID：5.8.4 抓包实测官方对话请求不带这两个头（它们只用于反馈上报），
// 带上「每账号恒定」的派生值反而是一处网关特征（hub #380 / PR #381 同口径）。
func TestChatHeadersDropMachineSessionIDs(t *testing.T) {
	c := New()
	a := &auth.Auth{UID: "u-drop", AccessToken: "at"}
	req := newReqWithHeaders(t)
	c.ChatHeaders(req, a, "", ChatMeta{})

	for _, k := range []string{"X-Machine-ID", "X-Session-ID"} {
		if v := req.Header.Get(k); v != "" {
			t.Errorf("chat 不得携带 %s（got %q）", k, v)
		}
	}
	// 对照组：反馈/计费类出站必须带（官方客户端在那里发）。
	breq := newReqWithHeaders(t)
	c.BillingHeaders(breq, a)
	for _, k := range []string{"X-Machine-ID", "X-Session-ID"} {
		if breq.Header.Get(k) == "" {
			t.Errorf("billing 应携带 %s", k)
		}
	}
}

// TestBillingMachineSessionIDsShapeAndIsolation 断言 billing 的两个设备头：
// 带连字符 UUID 形态、同账号稳定、跨账号互异（每账号一台虚拟设备——
// 取进程级值会让全池共用同一 session id，反而成为把账号串起来的关联键）。
func TestBillingMachineSessionIDsShapeAndIsolation(t *testing.T) {
	c := New()
	reqA1 := newReqWithHeaders(t)
	c.BillingHeaders(reqA1, &auth.Auth{UID: "u-iso-a", AccessToken: "at"})
	reqA2 := newReqWithHeaders(t)
	c.BillingHeaders(reqA2, &auth.Auth{UID: "u-iso-a", AccessToken: "at"})
	reqB := newReqWithHeaders(t)
	c.BillingHeaders(reqB, &auth.Auth{UID: "u-iso-b", AccessToken: "at"})

	for _, k := range []string{"X-Machine-ID", "X-Session-ID"} {
		v1, v2, v3 := reqA1.Header.Get(k), reqA2.Header.Get(k), reqB.Header.Get(k)
		if !uuidRe.MatchString(v1) {
			t.Errorf("%s=%q 不是带连字符的 UUID 形态", k, v1)
		}
		if v1 != v2 {
			t.Errorf("%s 同账号应稳定: %q != %q", k, v1, v2)
		}
		if v1 == v3 {
			t.Errorf("%s 跨账号必须互异（每账号一台设备）: %q", k, v1)
		}
	}
	// machine 与 session 是不同用途，不得同值。
	if reqA1.Header.Get("X-Machine-ID") == reqA1.Header.Get("X-Session-ID") {
		t.Error("X-Machine-ID 与 X-Session-ID 不得同值")
	}
	// uid 为空时不注入（匿名请求无设备标识）。
	empty := newReqWithHeaders(t)
	c.BillingHeaders(empty, &auth.Auth{AccessToken: "at"})
	if empty.Header.Get("X-Machine-ID") != "" {
		t.Error("uid 为空时不应注入 X-Machine-ID")
	}
}

// TestConversationHeaderFamilyShape 断言会话头族的形态与联动关系对齐 5.8.4 抓包：
// 会话 id 是带连字符 UUID，请求/消息/根请求 id 是 32 位 hex，
// 且 Message-ID == Request-ID、Root-Request-ID == Conversation-Request-ID。
func TestConversationHeaderFamilyShape(t *testing.T) {
	c := New()
	req := newReqWithHeaders(t)
	c.ChatHeaders(req, &auth.Auth{UID: "u-shape", AccessToken: "at"}, "", ChatMeta{})

	if v := req.Header.Get("X-Conversation-ID"); !uuidRe.MatchString(v) {
		t.Errorf("X-Conversation-ID=%q 不是带连字符 UUID 形态", v)
	}
	for _, k := range []string{"X-Conversation-Request-ID", "X-Conversation-Message-ID", "X-Request-ID", "X-Root-Request-ID"} {
		if v := req.Header.Get(k); !hex32Re.MatchString(v) {
			t.Errorf("%s=%q 不是 32 位 hex 形态", k, v)
		}
	}
	if req.Header.Get("X-Conversation-Message-ID") != req.Header.Get("X-Request-ID") {
		t.Error("X-Conversation-Message-ID 应等于 X-Request-ID")
	}
	if req.Header.Get("X-Root-Request-ID") != req.Header.Get("X-Conversation-Request-ID") {
		t.Error("X-Root-Request-ID 应等于 X-Conversation-Request-ID")
	}
	// B3 链路族仍齐全（TraceId 32hex、SpanId 16hex）。
	if v := req.Header.Get("X-B3-TraceId"); !hex32Re.MatchString(v) {
		t.Errorf("X-B3-TraceId=%q 非法", v)
	}
	if v := req.Header.Get("X-B3-SpanId"); len(v) != 16 {
		t.Errorf("X-B3-SpanId=%q len=%d want 16", v, len(v))
	}
}

// TestConversationHeadersSplitStrategy 断言两个 id 的**不同策略**（2026-10-16 定案）：
//   - X-Conversation-ID：每次出站现生成（客户端显式给了才透传）；
//   - X-Conversation-Request-ID / X-Root-Request-ID：按 meta 传入的轮级 id 复用
//     （handler 按轮键派生 → 一次 user send 内所有出站同值，issue #35 聚合语义）；
//   - X-Conversation-Message-ID / X-Request-ID：消息级，每次出站都新生成。
func TestConversationHeadersSplitStrategy(t *testing.T) {
	c := New()
	a := &auth.Auth{UID: "u-split", AccessToken: "at"}
	meta := ChatMeta{ConversationRequestID: "turn-id-32hex-placeholder"}

	r1, r2 := newReqWithHeaders(t), newReqWithHeaders(t)
	c.ChatHeaders(r1, a, "", meta)
	c.ChatHeaders(r2, a, "", meta)

	// 轮级 id：轮内复用（两次同值）。
	for _, k := range []string{"X-Conversation-Request-ID", "X-Root-Request-ID"} {
		if r1.Header.Get(k) != r2.Header.Get(k) {
			t.Errorf("%s 应轮内复用（同值）: %q != %q", k, r1.Header.Get(k), r2.Header.Get(k))
		}
		if got := r1.Header.Get(k); got != "turn-id-32hex-placeholder" {
			t.Errorf("%s 应原样使用传入的轮级 id, got %q", k, got)
		}
	}
	// 会话 id：每次现生成（两次不同）。
	if r1.Header.Get("X-Conversation-ID") == r2.Header.Get("X-Conversation-ID") {
		t.Error("X-Conversation-ID 应每次现生成（两次不得相同）")
	}
	// 消息级 id：每次出站都新生成（不复用）。
	for _, k := range []string{"X-Conversation-Message-ID", "X-Request-ID"} {
		if r1.Header.Get(k) == r2.Header.Get(k) {
			t.Errorf("%s 消息级应每次新生成: %q", k, r1.Header.Get(k))
		}
	}
	// 客户端显式给的会话 id 原样透传（真实客户端在同一对话内复用它）。
	r3 := newReqWithHeaders(t)
	c.ChatHeaders(r3, a, "", ChatMeta{ConversationID: "client-conv-abc"})
	if got := r3.Header.Get("X-Conversation-ID"); got != "client-conv-abc" {
		t.Errorf("客户端显式会话 id 应透传, got %q", got)
	}
	// 无轮级键（meta 未传）：现生成，两次不同（退化成本请求级随机）。
	r4, r5 := newReqWithHeaders(t), newReqWithHeaders(t)
	c.ChatHeaders(r4, a, "", ChatMeta{})
	c.ChatHeaders(r5, a, "", ChatMeta{})
	if r4.Header.Get("X-Conversation-Request-ID") == r5.Header.Get("X-Conversation-Request-ID") {
		t.Error("无轮级键时应现生成不同值")
	}
}

// TestPromptCacheKeyStableDespiteFreshConversationHeaders 断言**关键解耦**：
// 会话头族已改为每次现生成，但 prompt_cache_key 必须仍按 CacheKeySeed 稳定——
// 否则同一会话的连续请求各用一个新键、上游前缀缓存永不命中（P0 费用优化，
// 命中时费用降约 17×）。本用例走 ChatStream 全链路，抓出站 body 比对。
func TestPromptCacheKeyStableDespiteFreshConversationHeaders(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatHTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	a := &auth.Auth{AccessToken: "at", UID: "u-cache", ExpiresAt: 9999999999}
	body := []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"stream":true}`)

	// 同一 CacheKeySeed、不同 ConversationID（模拟「同一会话的连续两轮」）。
	for i := 0; i < 2; i++ {
		rc, _, _, err := c.ChatStream(a, body, "", ChatMeta{
			ConversationID: "fresh-" + string(rune('a'+i)),
			CacheKeySeed:   "stable-session-key",
		})
		if err != nil {
			t.Fatalf("chat %d: %v", i, err)
		}
		rc.Close()
	}
	if len(bodies) != 2 {
		t.Fatalf("captured bodies=%d want 2", len(bodies))
	}
	key := func(b string) string {
		var obj map[string]any
		if err := json.Unmarshal([]byte(b), &obj); err != nil {
			t.Fatalf("body 非法 JSON: %v", err)
		}
		s, _ := obj["prompt_cache_key"].(string)
		return s
	}
	k1, k2 := key(bodies[0]), key(bodies[1])
	if k1 == "" {
		t.Fatal("prompt_cache_key 未注入")
	}
	if k1 != k2 {
		t.Errorf("同一 CacheKeySeed 下 prompt_cache_key 必须稳定（否则前缀缓存永不命中）: %q != %q", k1, k2)
	}
	// 对照：换 CacheKeySeed 必须换键（不同会话不复用前缀）。
	rc, _, _, err := c.ChatStream(a, body, "", ChatMeta{CacheKeySeed: "another-session"})
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if k3 := key(bodies[2]); k3 == k1 {
		t.Errorf("不同 CacheKeySeed 应产生不同 prompt_cache_key: %q", k3)
	}
	// 键里仍带账号隔离段（跨账号绝不碰撞）。
	if !strings.Contains(k1, "wb2a-u-cache") {
		t.Errorf("cache key 缺账号隔离段: %q", k1)
	}
}

// TestDesktopChatConversationHeaders 断言桌面任务路径（DesktopChatWithExpert）
// 也补齐了会话头族——此前只手写 X-Conversation-ID + 十进制纳秒 X-Request-ID，
// 缺 -Request-ID/-Message-ID/X-Root-Request-ID 与链路头（hub #380 同型画像）。
func TestDesktopChatConversationHeaders(t *testing.T) {
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		hdr = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"0123456789abcdef0123456789abcdef\"}\n\n")
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	if _, _, err := c.DesktopChatWithExpert(&auth.Auth{AccessToken: "at", UID: "u-dt"}, ""); err != nil {
		t.Fatalf("DesktopChatWithExpert: %v", err)
	}
	for _, k := range []string{"X-Conversation-ID", "X-Conversation-Request-ID",
		"X-Conversation-Message-ID", "X-Root-Request-ID", "X-Request-ID", "X-Trace-ID",
		"X-B3-TraceId", "X-B3-SpanId"} {
		if hdr.Get(k) == "" {
			t.Errorf("桌面任务路径缺 %s", k)
		}
	}
	if v := hdr.Get("X-Request-ID"); !hex32Re.MatchString(v) {
		t.Errorf("X-Request-ID=%q 应为 32 位 hex（此前是十进制纳秒）", v)
	}
	if v := hdr.Get("X-Conversation-ID"); !uuidRe.MatchString(v) {
		t.Errorf("X-Conversation-ID=%q 应为带连字符 UUID", v)
	}
}
