//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayResponsesIsNativeCompactionV2(t *testing.T) {
	gin.SetMode(gin.TestMode)

	compactBody := `{"model":"claude-opus-5-5","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},{"type":"compaction_trigger"}]}`
	ordinaryBody := `{"model":"claude-opus-5-5","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	nonStreamBody := `{"model":"claude-opus-5-5","stream":false,"input":[{"type":"compaction_trigger"}]}`

	tests := []struct {
		name string
		path string
		body string
		want bool
	}{
		{name: "native v2 trigger on bare responses", path: "/v1/responses", body: compactBody, want: true},
		{name: "native v2 trigger on codex direct path", path: "/backend-api/codex/responses", body: compactBody, want: true},
		{name: "ordinary turn without trigger", path: "/v1/responses", body: ordinaryBody, want: false},
		{name: "non-streaming trigger", path: "/v1/responses", body: nonStreamBody, want: false},
		{name: "subpath is not the bare endpoint", path: "/v1/responses/compact", body: compactBody, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, tt.path, nil)
			require.Equal(t, tt.want, gatewayResponsesIsNativeCompactionV2(c, []byte(tt.body)))
		})
	}
}

func TestGatewayResponsesIsNativeCompactionV2NilSafe(t *testing.T) {
	require.False(t, gatewayResponsesIsNativeCompactionV2(nil, []byte(`{}`)))
}
