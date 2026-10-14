// probe.go 探活对话内容生成：随机算术题。
//
// 用途：所有「发一条真实但极短对话」的探活/任务路径共用同一题源——
//   - 夜猫子 RunNightChats（glm-5.2）；
//   - 桌面指纹 DesktopChatWithExpert（fast-model）；
//   - 面板 Model_chat_GLM5.2 runModelChat（glm-5.2，internal/panel 调用导出版）。
//
// 判据只要求存在一次成功对话，内容本身无关紧要。原实现各路径各自硬编码固定文案
// （"1+1等于几？直接回答。" / "hi，请回复一句话"）：同账号反复调用、多账号并发调用时
// 请求体逐字相同，是天然的「机器重复」指纹。改为每次随机生成一道算术题后，请求体
// 每次不同，而「单轮、极短、答案确定」的低消耗特性保持不变。
package upstream

import (
	"fmt"
	"math/rand/v2"
)

// probeQuestionSuffix 提问后缀（沿用原硬编码文案的收尾：指令短、回答短）。
const probeQuestionSuffix = "等于几？直接回答。"

// ProbeQuestion 生成一道随机算术题（题面 + probeQuestionSuffix）。
//
// 四种形状（覆盖加减乘与混合，避免单一运算符本身成为新指纹）：
//   - a + b      （10–99）
//   - a - b      （a 取 11–99、b 取 10–a-1，恒保 a > b，答案落在 [1, 89]）
//   - a × b      （2–12，小乘数保证心算快）
//   - a + b - c  （a/b 取 10–99、c 取 1–a+b-1，答案恒非负且非零）
//
// 不出除法：答案可能非整数，模型容易展开思考、拉长回答，与「极短对话」目标相悖。
// 随机源用 math/rand/v2 全局源（并发安全，无需自建锁）。
//
// 导出供 internal/panel 的任务动作复用（Model_chat_GLM5.2 等），保证全仓只有
// 一处题面逻辑——两套随机实现会随时间漂移成两种指纹。
func ProbeQuestion() string {
	switch rand.IntN(4) {
	case 0:
		return fmt.Sprintf("%d + %d %s", rand.IntN(90)+10, rand.IntN(90)+10, probeQuestionSuffix)
	case 1:
		a := rand.IntN(89) + 11   // 11–99
		b := rand.IntN(a-10) + 10 // 10–a-1
		return fmt.Sprintf("%d - %d %s", a, b, probeQuestionSuffix)
	case 2:
		return fmt.Sprintf("%d × %d %s", rand.IntN(11)+2, rand.IntN(11)+2, probeQuestionSuffix)
	default:
		a, b := rand.IntN(90)+10, rand.IntN(90)+10
		c := rand.IntN(a+b-1) + 1 // 1 ≤ c ≤ a+b-1 ⇒ 答案落在 [1, a+b-1]
		return fmt.Sprintf("%d + %d - %d %s", a, b, c, probeQuestionSuffix)
	}
}
