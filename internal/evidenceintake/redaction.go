package evidenceintake

import "regexp"

var redactionPatterns = []struct {
	re          *regexp.Regexp
	replacement string
}{
	{
		re:          regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:PASSWORD|PASSWD|PWD|TOKEN|SECRET|API[_-]?KEY|APP_KEY)[A-Z0-9_]*)\s*=\s*[^\s&]+`),
		replacement: `${1}=<redacted>`,
	},
	{
		re:          regexp.MustCompile(`(?i)\b(Bearer)\s+[A-Za-z0-9._~+/=-]+`),
		replacement: `${1} <redacted>`,
	},
	{
		re:          regexp.MustCompile(`(?i)\b(Basic)\s+[A-Za-z0-9._~+/=-]+`),
		replacement: `${1} <redacted>`,
	},
	{
		re:          regexp.MustCompile(`(://[^:\s/@]+:)[^@\s]+@`),
		replacement: `${1}<redacted>@`,
	},
}

func Redact(value string) string {
	out := value
	for _, pattern := range redactionPatterns {
		out = pattern.re.ReplaceAllString(out, pattern.replacement)
	}
	return out
}

func redactWithState(value string) (string, RedactionState) {
	redacted := Redact(value)
	if redacted != value {
		return redacted, RedactionRedacted
	}
	return redacted, RedactionNotNeeded
}
