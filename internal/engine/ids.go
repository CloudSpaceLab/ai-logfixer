package engine

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
)

type ContractIDFactory struct{}

func NewContractIDFactory() ContractIDFactory {
	return ContractIDFactory{}
}

func (ContractIDFactory) ID(prefix string, parts ...string) string {
	return StableID(prefix, parts...)
}

func StableID(prefix string, parts ...string) string {
	prefix = cleanIDPrefix(prefix)
	hash := sha1.New()
	for _, part := range parts {
		hash.Write([]byte(strings.TrimSpace(part)))
		hash.Write([]byte{0})
	}
	encoded := hex.EncodeToString(hash.Sum(nil))
	return prefix + "_" + encoded[:12]
}

func cleanIDPrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return "id"
	}
	var builder strings.Builder
	for _, char := range prefix {
		switch {
		case char >= 'a' && char <= 'z':
			builder.WriteRune(char)
		case char >= 'A' && char <= 'Z':
			builder.WriteRune(char + ('a' - 'A'))
		case char >= '0' && char <= '9':
			builder.WriteRune(char)
		case char == '_' || char == '-':
			builder.WriteRune('_')
		default:
			builder.WriteRune('_')
		}
	}
	cleaned := strings.Trim(builder.String(), "_")
	if cleaned == "" {
		return "id"
	}
	return cleaned
}
