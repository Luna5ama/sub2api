package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// stripMessageCacheControl 移除 $.messages[*].content[*].cache_control。
// 与 Parrot _strip_message_cache_control 语义一致。
//
// 旧策略为什么整体清空：客户端（特别是 Claude Code）经常把 cache_control 打在
// "当前最后一条 user message" 上；下一轮对话 messages 追加后，原本的最后一条
// 变成中间某条，cache_control 还挂着就导致"前缀签名变化"，破坏缓存命中。
// 统一由代理重新打断点（addMessageCacheBreakpoints）才能在多轮间稳定。
func stripMessageCacheControl(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	msgIdx := -1
	messages.ForEach(func(_, msg gjson.Result) bool {
		msgIdx++
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		blockIdx := -1
		content.ForEach(func(_, block gjson.Result) bool {
			blockIdx++
			if !block.Get("cache_control").Exists() {
				return true
			}
			path := fmt.Sprintf("messages.%d.content.%d.cache_control", msgIdx, blockIdx)
			if next, err := sjson.DeleteBytes(body, path); err == nil {
				body = next
			}
			return true
		})
		return true
	})
	return body
}

// addMessageCacheBreakpoints 在 messages 上注入两个稳定的 cache 断点：
//  1. 最后一条 message
//  2. 当 messages 数量 ≥ 4 时，倒数第二个 role=user 的 message
//
// 与 Parrot add_cache_breakpoints 一致。两个断点 + system prompt block 的断点
// + tools[-1] 的断点共同构成最多 4 个断点（Anthropic 上限）。
//
// cache_control ttl 策略：
//   - 若目标 block 已有 cache_control.ttl → 不覆盖
//   - 否则写入 {"type":"ephemeral","ttl": claude.DefaultCacheControlTTL}
//
// 调用前应先 stripMessageCacheControl 以保证幂等和稳定。
func addMessageCacheBreakpoints(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	arr := messages.Array()
	if len(arr) == 0 {
		return body
	}

	lastIdx := len(arr) - 1
	if arr[lastIdx].Get("role").String() == "assistant" {
		if idx, ok := lastSendableAssistantContentBlock(&arr[lastIdx]); ok {
			body = injectCacheControlOnContentBlock(body, lastIdx, idx)
		} else if !arr[lastIdx].Get("content").IsArray() {
			// Plain string content carries no thinking block, so it is promoted
			// to a text block by injectCacheControlOnLastContentBlock. Skipping
			// it here silently dropped the breakpoint for assistant turns.
			body = injectCacheControlOnLastContentBlock(body, lastIdx, &arr[lastIdx])
		}
	} else {
		body = injectCacheControlOnLastContentBlock(body, lastIdx, &arr[lastIdx])
	}

	// 2. 当 messages 数 ≥ 3 时，再在"倒数第二个 user 轮次"打断点。
	//
	// 这里必须从 ≥4 放宽到 ≥3：上游只会命中"当前请求自身打断了 cache_control
	// 的那个前缀"。Conversation 从 [u1] 变成 [u1,a1,u2] 时，上一轮写入的 u1 条目
	// 必须在这一轮被"重新声明"为断点，否则整段历史只能重新写缓存——实测线上
	// 表现为首轮写入约 2 万 token 后，紧接着的第二轮 cache_read 只有固定
	// system+tools 前缀（21252），其余 20177 全部重新计费为 cache_creation。
	// ≥3 时倒数第二个 user 恰好就是 u1，与上一轮的"最后一条 message"断点对齐，
	// 之后每轮都自然衔接。
	if len(arr) >= 3 {
		userCount := 0
		for i := len(arr) - 1; i >= 0; i-- {
			if arr[i].Get("role").String() != "user" {
				continue
			}
			userCount++
			if userCount == 2 {
				body = injectCacheControlOnLastContentBlock(body, i, &arr[i])
				break
			}
		}
	}

	return body
}

// rewriteMessageCacheControlIfEnabled 按系统设置决定是否执行旧版 messages 缓存断点改写。
func (s *GatewayService) rewriteMessageCacheControlIfEnabled(ctx context.Context, body []byte) []byte {
	if s == nil || !s.isRewriteMessageCacheControlEnabled(ctx) {
		return body
	}
	body = stripMessageCacheControl(body)
	return addMessageCacheBreakpoints(body)
}

// addResponsesAnthropicCacheBreakpoints gives Responses-originated Anthropic
// requests the same stable history anchors as the opt-in Messages rewrite.
// The inbound protocol cannot carry Anthropic cache_control, so stripping first
// is still defensive for bridge-injected history rather than a semantic change.
func addResponsesAnthropicCacheBreakpoints(body []byte) []byte {
	return addMessageCacheBreakpoints(stripMessageCacheControl(body))
}

// lastSendableAssistantContentBlock reports the index of the last assistant
// content block that may carry cache_control. Anthropic rejects cache_control on
// thinking blocks, so an all-thinking message has none. ok is false when no
// block qualifies, including when content is not a block array at all.
func lastSendableAssistantContentBlock(msg *gjson.Result) (int, bool) {
	content := msg.Get("content")
	if !content.IsArray() {
		return 0, false
	}
	blocks := content.Array()
	for i := len(blocks) - 1; i >= 0; i-- {
		blockType := blocks[i].Get("type").String()
		if blockType != "thinking" && blockType != "redacted_thinking" {
			return i, true
		}
	}
	return 0, false
}

func (s *GatewayService) isRewriteMessageCacheControlEnabled(ctx context.Context) bool {
	if s == nil {
		return false
	}
	if s.settingService != nil {
		return s.settingService.IsRewriteMessageCacheControlEnabled(ctx)
	}
	return false
}

// injectCacheControlOnLastContentBlock 把 cache_control 断点打在 messages[idx]
// 的最后一个 content block 上。若 content 是 string，先升级成单块 text 数组
// （对齐 Parrot _inject_cache_on_msg 的行为）。
//
// msg 是调用方已持有的 gjson.Result 快照，用于省一次 GetBytes。
func injectCacheControlOnLastContentBlock(body []byte, idx int, msg *gjson.Result) []byte {
	content := msg.Get("content")

	if content.Type == gjson.String {
		text := content.String()
		blockRaw := fmt.Sprintf(
			`[{"type":"text","text":%s,"cache_control":{"type":"ephemeral","ttl":%q}}]`,
			mustJSONString(text), claude.DefaultCacheControlTTL,
		)
		if next, err := sjson.SetRawBytes(body, fmt.Sprintf("messages.%d.content", idx), []byte(blockRaw)); err == nil {
			body = next
		}
		return body
	}

	if !content.IsArray() {
		return body
	}
	contentArr := content.Array()
	if len(contentArr) == 0 {
		return body
	}
	return injectCacheControlOnContentBlock(body, idx, len(contentArr)-1)
}

func injectCacheControlOnContentBlock(body []byte, idx, blockIdx int) []byte {
	block := gjson.GetBytes(body, fmt.Sprintf("messages.%d.content.%d", idx, blockIdx))
	if !block.Exists() {
		return body
	}
	if cc := block.Get("cache_control"); cc.Exists() && cc.Get("ttl").String() != "" {
		return body
	}

	pathPrefix := fmt.Sprintf("messages.%d.content.%d.cache_control", idx, blockIdx)
	existingCC := block.Get("cache_control")
	if existingCC.Exists() {
		if next, err := sjson.SetBytes(body, pathPrefix+".ttl", claude.DefaultCacheControlTTL); err == nil {
			body = next
		}
		return body
	}
	raw := fmt.Sprintf(`{"type":"ephemeral","ttl":%q}`, claude.DefaultCacheControlTTL)
	if next, err := sjson.SetRawBytes(body, pathPrefix, []byte(raw)); err == nil {
		body = next
	}
	return body
}

// mustJSONString 把一个 Go string 序列化为合法 JSON string（含引号），
// 用于 sjson.SetRawBytes 场景下手工拼 JSON。
func mustJSONString(s string) string {
	// Go string quoting can emit non-JSON escapes such as \x7f or \a.
	// Marshaling a string cannot fail.
	encoded, _ := json.Marshal(s)
	return string(encoded)
}
