package apicompat

import (
	"encoding/base64"
	"strings"
)

const plaintextCompactionEnvelopePrefix = "sub2api-plaintext-v1:"

func encodePlaintextCompactionSummary(summary string) string {
	return plaintextCompactionEnvelopePrefix + base64.StdEncoding.EncodeToString([]byte(summary))
}

func decodePlaintextCompactionSummary(encryptedContent string) (string, bool) {
	if !strings.HasPrefix(encryptedContent, plaintextCompactionEnvelopePrefix) {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encryptedContent, plaintextCompactionEnvelopePrefix))
	if err != nil {
		return "", false
	}
	return string(decoded), true
}
