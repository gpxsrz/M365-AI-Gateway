package web

import "strings"

const (
	artifactRoutePrefix = "/v1/artifacts/"
	artifactRouteSuffix = "/content"
	artifactTokenLength = 43 // base64url without padding for 32 random bytes
)

func artifactCapabilityToken(path string) (string, bool) {
	if !strings.HasPrefix(path, artifactRoutePrefix) || !strings.HasSuffix(path, artifactRouteSuffix) {
		return "", false
	}
	token := strings.TrimSuffix(strings.TrimPrefix(path, artifactRoutePrefix), artifactRouteSuffix)
	if len(token) != artifactTokenLength || strings.Contains(token, "/") {
		return "", false
	}
	for _, char := range token {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return "", false
	}
	return token, true
}
