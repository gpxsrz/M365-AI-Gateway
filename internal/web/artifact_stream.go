package web

import "strings"

var protectedStreamMarkers = []string{
	"https://",
	"http://",
	"blob:",
	"coderesultfileurl",
	"asyncgw.teams.microsoft.com",
}

// releaseArtifactSafePrefix keeps every URL (and any partial marker prefix)
// until the final structured result can distinguish a public link from a
// protected Microsoft artifact URL. Ordinary text continues to stream.
func releaseArtifactSafePrefix(buffer *strings.Builder) string {
	value := buffer.String()
	holdAt := len(value)
	for _, marker := range protectedStreamMarkers {
		if index := indexASCIIFold(value, marker); index >= 0 && index < holdAt {
			holdAt = index
		}
	}
	if holdAt == len(value) {
		keep := 0
		for _, marker := range protectedStreamMarkers {
			for length := 1; length < len(marker) && length <= len(value); length++ {
				if equalASCIIFold(value[len(value)-length:], marker[:length]) && length > keep {
					keep = length
				}
			}
		}
		holdAt -= keep
	}
	released := value[:holdAt]
	held := value[holdAt:]
	buffer.Reset()
	buffer.WriteString(held)
	return released
}

func indexASCIIFold(value, marker string) int {
	for start := 0; start+len(marker) <= len(value); start++ {
		if asciiLower(value[start]) == marker[0] && equalASCIIFold(value[start:start+len(marker)], marker) {
			return start
		}
	}
	return -1
}

func equalASCIIFold(value, marker string) bool {
	if len(value) != len(marker) {
		return false
	}
	for index := range value {
		if asciiLower(value[index]) != marker[index] {
			return false
		}
	}
	return true
}

func asciiLower(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}
