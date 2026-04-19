package configgen

import (
	"regexp"
)

// ParsedConfig holds secret values extracted from a live configuration.py.
type ParsedConfig struct {
	SecretKey            string
	APITokenPepper       string
	DBUser               string
	DBPassword           string
	RedisTasksPassword   string
	RedisCachingPassword string
}

var (
	reSecretKey = regexp.MustCompile(`(?m)^SECRET_KEY\s*=\s*['"]([^'"]+)['"]`)

	// Matches both {0: 'val'} and {1: 'val'} style peppers — capture the first value.
	reAPITokenPepper = regexp.MustCompile(`(?m)API_TOKEN_PEPPERS\s*=\s*\{[^}]*\d+\s*:\s*['"]([^'"]+)['"]`)

	reRedisBlockPassword = regexp.MustCompile(`(?s)'PASSWORD'\s*:\s*['"]([^'"]*)['"]\s*,`)
)

// ParseNetboxConfig extracts credential values from the raw text of configuration.py.
// It handles both the conductor-generated format (DATABASE = {...}) and the standard
// NetBox format (DATABASES = {'default': {...}}).
// Fields that cannot be found are returned as empty strings.
func ParseNetboxConfig(content string) ParsedConfig {
	var p ParsedConfig

	if m := reSecretKey.FindStringSubmatch(content); len(m) > 1 {
		p.SecretKey = m[1]
	}
	if m := reAPITokenPepper.FindStringSubmatch(content); len(m) > 1 {
		p.APITokenPepper = m[1]
	}

	// Try conductor-generated format (DATABASE = {...}) first, then standard Django
	// format (DATABASES = {'default': {...}}).
	if dbBlock := extractTopLevelBlock(content, "DATABASE"); dbBlock != "" {
		p.DBUser = extractUserFromBlock(dbBlock)
		p.DBPassword = extractPasswordFromBlock(dbBlock)
	} else if dbBlock := extractDefaultBlock(content); dbBlock != "" {
		p.DBUser = extractUserFromBlock(dbBlock)
		p.DBPassword = extractPasswordFromBlock(dbBlock)
	}

	if tasksBlock := extractRedisSubBlock(content, "tasks"); tasksBlock != "" {
		if m := reRedisBlockPassword.FindStringSubmatch(tasksBlock); len(m) > 1 {
			p.RedisTasksPassword = m[1]
		}
	}
	if cachingBlock := extractRedisSubBlock(content, "caching"); cachingBlock != "" {
		if m := reRedisBlockPassword.FindStringSubmatch(cachingBlock); len(m) > 1 {
			p.RedisCachingPassword = m[1]
		}
	}

	return p
}

var rePasswordValue = regexp.MustCompile(`'PASSWORD'\s*:\s*['"]([^'"]*)['"]\s*,?`)
var reUserValue = regexp.MustCompile(`'USER'\s*:\s*['"]([^'"]*)['"]\s*,?`)

func extractPasswordFromBlock(block string) string {
	if m := rePasswordValue.FindStringSubmatch(block); len(m) > 1 {
		return m[1]
	}
	return ""
}

func extractUserFromBlock(block string) string {
	if m := reUserValue.FindStringSubmatch(block); len(m) > 1 {
		return m[1]
	}
	return ""
}

// extractTopLevelBlock finds `KEY = {` and returns the content of the braces.
func extractTopLevelBlock(content, key string) string {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `\s*=\s*\{`)
	loc := re.FindStringIndex(content)
	if loc == nil {
		return ""
	}
	return extractBraces(content, loc[1]-1)
}

// extractDefaultBlock finds DATABASES = {'default': {...}} and returns the inner block.
func extractDefaultBlock(content string) string {
	outer := extractTopLevelBlock(content, "DATABASES")
	if outer == "" {
		return ""
	}
	re := regexp.MustCompile(`'default'\s*:\s*\{`)
	loc := re.FindStringIndex(outer)
	if loc == nil {
		return ""
	}
	return extractBraces(outer, loc[1]-1)
}

// extractRedisSubBlock finds REDIS = {...} and within it extracts the named sub-block.
func extractRedisSubBlock(content, subkey string) string {
	redisBlock := extractTopLevelBlock(content, "REDIS")
	if redisBlock == "" {
		return ""
	}
	re := regexp.MustCompile(`'` + regexp.QuoteMeta(subkey) + `'\s*:\s*\{`)
	loc := re.FindStringIndex(redisBlock)
	if loc == nil {
		return ""
	}
	return extractBraces(redisBlock, loc[1]-1)
}

// extractBraces returns the text from opening brace at start through its matching closing brace.
func extractBraces(s string, start int) string {
	if start >= len(s) || s[start] != '{' {
		return ""
	}
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
