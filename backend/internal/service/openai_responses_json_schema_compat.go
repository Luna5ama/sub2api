package service

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Structured output (Responses `text.format` with type=json_schema) only stays
// enforceable when the upstream itself enforces the schema. A real OpenAI
// Responses endpoint does that even when the request also carries tools. Most
// other upstreams do not: as soon as `tools` is present they accept the
// `text.format` / `response_format` field but stop applying the constraint and
// answer in free-form prose.
//
// That breaks clients which rely on the schema. Codex generates thread titles
// by starting a hidden ephemeral thread (`thread_source=thread_title`) with a
// strict {title, description} schema, then validates the returned assistant
// text. When the upstream ignores the schema the reply is prose, validation
// fails silently, and the thread keeps the raw prompt as its name.
//
// The mitigation below restates the client-declared schema in `instructions`
// so upstreams that do not enforce `text.format` still get the same contract
// from the prompt.

// openAIUpstreamReliablyEnforcesJSONSchema reports whether the account's
// upstream can be trusted to enforce a `text.format` JSON Schema server-side.
//
// Only genuine OpenAI Responses endpoints qualify: api.openai.com and the
// ChatGPT Codex backend both honor `text.format` while tools are declared.
// Everything else gets the prompt-side restatement, because the constraint has
// been observed to be dropped once tools are present on:
//   - custom base_url relays in front of an OpenAI-shaped endpoint
//   - DeepSeek's native Responses endpoint
//   - the Responses-to-Chat and Responses-to-Anthropic bridges
//
// Treating an unrecognized upstream as "does not enforce" is the fail-safe
// direction: the added instruction only restates a contract the client already
// asked for, whereas a dropped constraint silently returns prose to a client
// that cannot parse it.
func openAIUpstreamReliablyEnforcesJSONSchema(account *Account) bool {
	if account == nil {
		return true
	}
	baseURL := strings.TrimSpace(account.GetOpenAIBaseURL())
	if baseURL == "" {
		// Accounts outside the OpenAI protocol family (Grok, Anthropic) expose
		// no OpenAI base URL here, so the host cannot be checked.
		return false
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "api.openai.com", "chatgpt.com":
		return true
	default:
		return false
	}
}

// needsOpenAIResponsesJSONSchemaInstruction limits the rewrite to the exact
// combination that regresses: the client declared a json_schema format, the
// request carries tools, and the upstream is not trusted to enforce it.
// Without tools every upstream applies the schema on its own, so no extra
// instruction is added and unrelated requests stay byte-identical.
func needsOpenAIResponsesJSONSchemaInstruction(account *Account, body []byte) bool {
	if len(body) == 0 || openAIUpstreamReliablyEnforcesJSONSchema(account) {
		return false
	}
	if strings.TrimSpace(gjson.GetBytes(body, "text.format.type").String()) != "json_schema" {
		return false
	}
	return openAIRequestBodyHasTools(body)
}

// reinforceOpenAIResponsesJSONSchemaInstructions appends the client-declared
// schema to `instructions`. changed=false means the request did not need the
// rewrite (or already carried it).
func reinforceOpenAIResponsesJSONSchemaInstructions(account *Account, body []byte) ([]byte, bool, error) {
	if !needsOpenAIResponsesJSONSchemaInstruction(account, body) {
		return body, false, nil
	}
	schema := openAIResponsesDeclaredTextSchema(body)
	if schema == "" {
		return body, false, nil
	}
	directive := openAIResponsesJSONSchemaInstruction(schema)

	instructions := strings.TrimSpace(gjson.GetBytes(body, "instructions").String())
	// Codex replays the same instructions on continuation turns; appending the
	// directive again would grow the prompt on every turn.
	if strings.Contains(instructions, directive) {
		return body, false, nil
	}
	combined := directive
	if instructions != "" {
		combined = instructions + "\n\n" + directive
	}
	updated, err := sjson.SetBytes(body, "instructions", combined)
	if err != nil {
		return body, false, err
	}
	return updated, true, nil
}

// openAIResponsesDeclaredTextSchema extracts the declared JSON Schema. The
// Responses shape nests it under text.format.schema; a body already written in
// Chat shape nests it under text.format.json_schema.schema.
func openAIResponsesDeclaredTextSchema(body []byte) string {
	if schema := strings.TrimSpace(gjson.GetBytes(body, "text.format.schema").Raw); schema != "" {
		return schema
	}
	return strings.TrimSpace(gjson.GetBytes(body, "text.format.json_schema.schema").Raw)
}

func openAIResponsesJSONSchemaInstruction(schema string) string {
	if compacted := compactOpenAIJSONSchema(schema); compacted != "" {
		schema = compacted
	}
	// Only the final assistant text is constrained. Tool calls stay allowed:
	// Codex's title task explicitly tells the model to consult a read-only tool
	// first when the prompt points at an opaque resource, so forbidding tool
	// calls here would break that behavior.
	return "Structured output requirement: when you produce assistant text, it MUST be a single JSON object that validates against this JSON Schema, " +
		"with no surrounding prose and no markdown code fences. Only emit that JSON object; do not add anything around it.\n" + schema
}

// compactOpenAIJSONSchema flattens the schema so the caller's indentation is not
// copied verbatim into instructions. It returns "" when the schema does not
// parse, letting the caller keep the original text.
func compactOpenAIJSONSchema(raw string) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(raw)); err != nil {
		return ""
	}
	return buf.String()
}
