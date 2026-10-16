package pool

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestStatusDeviceTokenDesensitized 断言账号状态里的设备码**只透出脱敏摘要**：
// 有无 + 前 4 位 + 长度；完整 token 绝不出现在 Status 的任何字段里（含 JSON 序列化）。
//
// 为什么值得一条用例：面板是运维视图，X-Device-Token 原值等于设备指纹——一旦
// 有人后续把原值塞进某个字段（或改成透传整个 token 让前端截断），这条断言立刻
// 失败。脱敏必须在 pool 边界完成，前端拿不到原值就没有「前端忘了脱敏」这条面。
func TestStatusDeviceTokenDesensitized(t *testing.T) {
	const full = "wb2a-device-token-SECRET-abcdef1234567890"

	p := New("")
	p.Add(&auth.Auth{UID: "u-dev", AccessToken: "at", DeviceToken: full})
	p.Add(&auth.Auth{UID: "u-none", AccessToken: "at"}) // 未自带设备码

	list := p.List()
	if len(list) != 2 {
		t.Fatalf("accounts=%d want 2", len(list))
	}
	byUID := map[string]Status{}
	for _, s := range list {
		byUID[s.UID] = s
	}

	got := byUID["u-dev"]
	if got.DeviceTokenLen != len(full) {
		t.Errorf("DeviceTokenLen=%d want %d", got.DeviceTokenLen, len(full))
	}
	if got.DeviceTokenPrefix != full[:4] {
		t.Errorf("DeviceTokenPrefix=%q want %q", got.DeviceTokenPrefix, full[:4])
	}

	none := byUID["u-none"]
	if none.DeviceTokenLen != 0 || none.DeviceTokenPrefix != "" {
		t.Errorf("无设备码的号不应透出摘要: prefix=%q len=%d", none.DeviceTokenPrefix, none.DeviceTokenLen)
	}

	// 关键断言：序列化后完整 token（及前 4 位之外的部分）都不得出现。
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), full) {
		t.Fatalf("完整 device token 泄漏进 Status JSON: %s", raw)
	}
	if strings.Contains(string(raw), full[4:]) {
		t.Fatalf("device token 前 4 位之外的部分泄漏进 Status JSON: %s", raw)
	}
}

// TestDeviceTokenHint 边界：空、极短（≤4）、含多字节字符（按 rune 截取不切半个字）。
func TestDeviceTokenHint(t *testing.T) {
	cases := []struct {
		in      string
		wantPfx string
		wantLen int
	}{
		{"", "", 0},
		{"ab", "ab", 2},
		{"abcd", "abcd", 4},
		{"abcde", "abcd", 5},
		{"设备码一二三", "设备码一", 6}, // 6 个 rune，前 4 个整字
	}
	for _, c := range cases {
		pfx, n := deviceTokenHint(c.in)
		if pfx != c.wantPfx || n != c.wantLen {
			t.Errorf("deviceTokenHint(%q) = (%q,%d) want (%q,%d)", c.in, pfx, n, c.wantPfx, c.wantLen)
		}
	}
}
