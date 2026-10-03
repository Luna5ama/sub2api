package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const testTitleSchemaBody = `{
	"model":"gpt-6-luna",
	"instructions":"You are a helpful assistant that writes a short task title.",
	"input":[{"role":"user","content":"好像有什么不对劲"}],
	"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}],
	"tool_choice":"auto",
	"text":{"format":{"type":"json_schema","name":"codex_output_schema","strict":true,"schema":{
		"type":"object",
		"properties":{"title":{"type":"string","minLength":1,"maxLength":36},"description":{"type":"string","minLength":1}},
		"required":["title","description"],
		"additionalProperties":false
	}}}
}`

func bridgedTitleAccount() *Account {
	return &Account{
		ID:       1,
		Name:     "bridged",
		Platform: PlatformDeepseek,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":      "sk-test",
			"api_protocol": APIProtocolResponses,
		},
	}
}

// customBaseURLAccount models the deployment shape that surfaced the bug: an
// openai-platform account whose base_url points at a relay that only honors
// text.format when no tools are declared.
func customBaseURLAccount() *Account {
	return &Account{
		ID:       3,
		Name:     "relay",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://relay.example.invalid/v1",
		},
	}
}

// 这是修复的主回归：带 tools 的 Responses 结构化输出请求打到桥接型上游时，
// 之前 text.format 的 schema 约束不会被执行，模型退回自由文本，Codex 因此
// 拿不到合法标题。修复后 schema 必须被补进 instructions。
func TestReinforceOpenAIResponsesJSONSchemaInstructions_AddsSchemaForBridgedUpstreamWithTools(t *testing.T) {
	body := []byte(testTitleSchemaBody)

	for name, account := range map[string]*Account{
		"deepseek-native-responses": bridgedTitleAccount(),
		"custom-base-url-relay":     customBaseURLAccount(),
	} {
		t.Run(name, func(t *testing.T) {
			updated, changed, err := reinforceOpenAIResponsesJSONSchemaInstructions(account, body)
			require.NoError(t, err)
			require.True(t, changed)

			instructions := gjson.GetBytes(updated, "instructions").String()
			require.True(t, strings.HasPrefix(instructions, "You are a helpful assistant"))
			require.Contains(t, instructions, "Structured output requirement")
			require.Contains(t, instructions, `"maxLength":36`)
			require.Contains(t, instructions, `"additionalProperties":false`)

			// schema 之外的请求字段必须原样保留，text.format 也不能被改写。
			require.Equal(t, "gpt-6-luna", gjson.GetBytes(updated, "model").String())
			require.Equal(t, "json_schema", gjson.GetBytes(updated, "text.format.type").String())
			require.Equal(t, "codex_output_schema", gjson.GetBytes(updated, "text.format.name").String())
			require.True(t, gjson.GetBytes(updated, "text.format.strict").Bool())
			require.Equal(t, int64(36), gjson.GetBytes(updated, "text.format.schema.properties.title.maxLength").Int())
			require.Len(t, gjson.GetBytes(updated, "tools").Array(), 1)
			require.Equal(t, "auto", gjson.GetBytes(updated, "tool_choice").String())
		})
	}
}

// A genuine OpenAI Responses endpoint enforces text.format even with tools, so
// it must not receive the redundant instruction.
func TestReinforceOpenAIResponsesJSONSchemaInstructions_SkipsNativeOpenAIUpstream(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baseURL string
	}{
		{"api.openai.com", "https://api.openai.com"},
		{"chatgpt.com", "https://chatgpt.com"},
		// Default base_url for an openai account without an explicit one.
		{"default", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{
				ID:       2,
				Name:     "native",
				Platform: PlatformOpenAI,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"api_key": "sk-test",
				},
			}
			if tc.baseURL != "" {
				account.Credentials["base_url"] = tc.baseURL
			}
			updated, changed, err := reinforceOpenAIResponsesJSONSchemaInstructions(account, []byte(testTitleSchemaBody))
			require.NoError(t, err)
			require.False(t, changed)
			require.JSONEq(t, testTitleSchemaBody, string(updated))
		})
	}
}

// nil account must not panic and must not rewrite.
func TestReinforceOpenAIResponsesJSONSchemaInstructions_NilAccount(t *testing.T) {
	updated, changed, err := reinforceOpenAIResponsesJSONSchemaInstructions(nil, []byte(testTitleSchemaBody))
	require.NoError(t, err)
	require.False(t, changed)
	require.JSONEq(t, testTitleSchemaBody, string(updated))
}

// 没有 tools 时上游会正常执行 schema 约束，不注入指令，避免扰动其它请求。
func TestReinforceOpenAIResponsesJSONSchemaInstructions_SkipsWithoutTools(t *testing.T) {
	body := []byte(`{"model":"gpt-6-luna","input":"hi","text":{"format":{"type":"json_schema","name":"s","strict":true,"schema":{"type":"object"}}}}`)

	updated, changed, err := reinforceOpenAIResponsesJSONSchemaInstructions(bridgedTitleAccount(), body)
	require.NoError(t, err)
	require.False(t, changed)
	require.JSONEq(t, string(body), string(updated))
}

// 非结构化输出请求（没有 text.format）不受影响。
func TestReinforceOpenAIResponsesJSONSchemaInstructions_SkipsNonJSONSchemaRequests(t *testing.T) {
	for name, body := range map[string]string{
		"no-text-format":      `{"model":"gpt-6-luna","input":"hi","tools":[{"type":"function","name":"t","parameters":{"type":"object"}}]}`,
		"json-object-mode":    `{"model":"gpt-6-luna","input":"hi","tools":[{"type":"function","name":"t","parameters":{"type":"object"}}],"text":{"format":{"type":"json_object"}}}`,
		"text-with-verbosity": `{"model":"gpt-6-luna","input":"hi","tools":[{"type":"function","name":"t","parameters":{"type":"object"}}],"text":{"verbosity":"low"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			updated, changed, err := reinforceOpenAIResponsesJSONSchemaInstructions(bridgedTitleAccount(), []byte(body))
			require.NoError(t, err)
			require.False(t, changed)
			require.JSONEq(t, body, string(updated))
		})
	}
}

// tools 以 input[].additional_tools 形式声明时同样要注入。
func TestReinforceOpenAIResponsesJSONSchemaInstructions_DetectsAdditionalToolsItem(t *testing.T) {
	body := []byte(`{
		"model":"gpt-6-luna",
		"input":[{"role":"user","content":"hi"},{"type":"additional_tools","tools":[{"type":"function","name":"t","parameters":{"type":"object"}}]}],
		"text":{"format":{"type":"json_schema","name":"s","strict":true,"schema":{"type":"object","properties":{"a":{"type":"string"}}}}}
	}`)

	updated, changed, err := reinforceOpenAIResponsesJSONSchemaInstructions(bridgedTitleAccount(), body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, gjson.GetBytes(updated, "instructions").String(), "Structured output requirement")
}

// 续链请求会回放同一份 instructions，重复注入必须被抑制。
func TestReinforceOpenAIResponsesJSONSchemaInstructions_IsIdempotent(t *testing.T) {
	once, changed, err := reinforceOpenAIResponsesJSONSchemaInstructions(bridgedTitleAccount(), []byte(testTitleSchemaBody))
	require.NoError(t, err)
	require.True(t, changed)

	twice, changedAgain, err := reinforceOpenAIResponsesJSONSchemaInstructions(bridgedTitleAccount(), once)
	require.NoError(t, err)
	require.False(t, changedAgain)
	require.JSONEq(t, string(once), string(twice))
}

// 已经写成 Chat 形态的 text.format.json_schema.schema 也要能取到 schema。
func TestOpenAIResponsesDeclaredTextSchema_SupportsChatShape(t *testing.T) {
	body := []byte(`{"text":{"format":{"type":"json_schema","json_schema":{"name":"s","schema":{"type":"object","properties":{"a":{"type":"string"}}}}}}}`)
	require.Equal(t, `{"type":"object","properties":{"a":{"type":"string"}}}`, openAIResponsesDeclaredTextSchema(body))
}

// schema 会展平成单行，避免把调用方的缩进原样复制进 instructions。
func TestOpenAIResponsesJSONSchemaInstruction_CompactsSchema(t *testing.T) {
	directive := openAIResponsesJSONSchemaInstruction("{\n  \"type\": \"object\",\n  \"properties\": {\n    \"title\": {\n      \"type\": \"string\"\n    }\n  }\n}")
	require.Contains(t, directive, `{"type":"object","properties":{"title":{"type":"string"}}}`)
	require.NotContains(t, directive, "\n  ")
}

// 端到端：/v1/responses 请求打到只支持 Chat Completions 的桥接账号时，出站
// Chat 请求体里同时保留 response_format 与补进 instructions 的 schema 指令，
// 工具列表不受影响。
func TestForwardResponsesViaChatCompletions_KeepsSchemaConstraintWithTools(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(testTitleSchemaBody))

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl_1","choices":[{"message":{"role":"assistant","content":"{\"title\":\"T\",\"description\":\"D\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		)),
	}}

	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID: 1, Name: "chat-bridge", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Extra: map[string]any{"openai_responses_supported": false},
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://bridge.example.invalid",
		},
	}

	_, err := svc.Forward(context.Background(), c, account, []byte(testTitleSchemaBody))
	require.NoError(t, err)

	outbound := upstream.lastBody
	require.NotEmpty(t, outbound)

	// Chat 形态的 response_format 必须仍在。
	require.Equal(t, "json_schema", gjson.GetBytes(outbound, "response_format.type").String())
	require.Equal(t, "codex_output_schema", gjson.GetBytes(outbound, "response_format.json_schema.name").String())
	require.Len(t, gjson.GetBytes(outbound, "tools").Array(), 1)

	// 关键：schema 约束随请求一起送出。Responses→Chat 桥会把 instructions
	// 提升成首条 system message。
	system := gjson.GetBytes(outbound, "messages.0.content").String()
	require.Equal(t, "system", gjson.GetBytes(outbound, "messages.0.role").String())
	require.Contains(t, system, "You are a helpful assistant")
	require.Contains(t, system, "Structured output requirement")
	require.Contains(t, system, `"maxLength":36`)
	require.Contains(t, system, `"additionalProperties":false`)
}

// End-to-end through the Responses passthrough branch (custom base_url relay,
// the deployment shape that surfaced the bug): the outbound Responses body must
// carry the schema restatement in instructions while keeping text.format and
// tools untouched.
func TestForwardOpenAIPassthrough_ReinforcesSchemaConstraintWithTools(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(testTitleSchemaBody))

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_1","object":"response","created_at":1,"model":"gpt-6-luna","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"title\":\"T\",\"description\":\"D\"}"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
		)),
	}}

	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := customBaseURLAccount()
	account.Extra = map[string]any{"openai_passthrough": true}
	account.Concurrency = 1

	_, err := svc.Forward(context.Background(), c, account, []byte(testTitleSchemaBody))
	require.NoError(t, err)

	outbound := upstream.lastBody
	require.NotEmpty(t, outbound)
	require.Equal(t, "json_schema", gjson.GetBytes(outbound, "text.format.type").String())
	require.Len(t, gjson.GetBytes(outbound, "tools").Array(), 1)

	instructions := gjson.GetBytes(outbound, "instructions").String()
	require.Contains(t, instructions, "You are a helpful assistant")
	require.Contains(t, instructions, "Structured output requirement")
	require.Contains(t, instructions, `"maxLength":36`)
}
