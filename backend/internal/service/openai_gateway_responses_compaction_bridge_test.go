//go:build unit

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestResponsesStreamingFromNativeAnthropic_CompactionOnlyEmitsSingleItem(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newNativeAnthropicHangTestService(5)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	MarkOpenAINativeCompactionV2(c)

	lines := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_compact","type":"message","role":"assistant","content":[],"model":"glm-5.3","usage":{"input_tokens":12,"output_tokens":6}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"<summary>continue</summary>"}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(lines))}

	_, err := svc.handleResponsesStreamingFromNativeAnthropic(
		resp, c, "glm-5.3", "glm-5.3", "glm-5.3", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), `"type":"response.created"`)
	require.Contains(t, rec.Body.String(), `"type":"compaction"`)
	require.Contains(t, rec.Body.String(), `sub2api-plaintext-v1:`)
	require.NotContains(t, rec.Body.String(), `"type":"message"`)
	require.NotContains(t, rec.Body.String(), `"type":"reasoning"`)
}
