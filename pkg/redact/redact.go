package redact

import (
	"math"
	"regexp"
	"strings"
	"sync"
)

// Rule replaces matches of a pattern. If the pattern has a group named
// "secret", only that group is replaced (so "API_KEY=" or "Bearer " stays
// readable); otherwise the whole match is.
type Rule struct {
	Kind    string
	Pattern *regexp.Regexp
}

// Builtin returns the built-in rules, most specific first. Patterns follow
// the formats the vendors document (and common secret-scanner rules).
// They're compiled on first use: the hook binary never pays for them.
var Builtin = sync.OnceValue(func() []Rule {
	return []Rule{
		{"private_key", regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|\z)`)},
		{"anthropic_key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{20,}`)},
		{"openai_key", regexp.MustCompile(`\bsk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_\-]{20,}`)},
		{"github_token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,255}|github_pat_[A-Za-z0-9_]{22,255})\b`)},
		{"gitlab_token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20,}`)},
		{"aws_access_key", regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA)[0-9A-Z]{16}\b`)},
		{"slack_token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`)},
		{"slack_webhook", regexp.MustCompile(`https://hooks\.slack\.com/services/[A-Za-z0-9/_\-]+`)},
		{"stripe_key", regexp.MustCompile(`\b(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{16,}`)},
		{"google_api_key", regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`)},
		{"npm_token", regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`)},
		{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`)},
		{"url_password", regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.\-]*://[^\s:/@]+:(?P<secret>[^\s@/]+)@`)},
		{"bearer_token", regexp.MustCompile(`(?i)\b(?:bearer|token)\s+(?P<secret>[A-Za-z0-9\-._~+/]{20,}=*)`)},
		{"secret_flag", regexp.MustCompile(`(?i)--(?:password|passwd|token|secret|api-key|apikey|access-key)(?:=|\s+)(?P<secret>"[^"]*"|'[^']*'|\S+)`)},
		{"secret_json", regexp.MustCompile(`(?i)"[a-z0-9_.\-]*(?:secret|token|password|passwd|api_?key|access_?key|private_?key)[a-z0-9_.\-]*"\s*:\s*"(?P<secret>[^"]+)"`)},
		{"secret_assignment", regexp.MustCompile(`(?i)\b[A-Z0-9_]*(?:SECRET|TOKEN|PASSWORD|PASSWD|PWD|API_?KEY|ACCESS_?KEY|PRIVATE_?KEY|CREDENTIALS?)[A-Z0-9_]*\s*[=:]\s*(?P<secret>"[^"]+"|'[^']+'|[^\s'"]+)`)},
	}
})

var entropyCandidate = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`[A-Za-z0-9_+=\-]{24,}`) })

// Redactor applies the built-in rules plus custom ones.
type Redactor struct {
	custom []Rule
}

// New returns a redactor with the built-in rules plus extra patterns (from
// user config). Invalid extra patterns are reported, not silently dropped.
func New(extra []string) (*Redactor, error) {
	r := &Redactor{}
	for _, p := range extra {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, err
		}
		r.custom = append(r.custom, Rule{Kind: "custom", Pattern: re})
	}
	return r, nil
}

// Default is a redactor with the built-in rules only.
var Default = &Redactor{}

// Marker is what replaces a secret, e.g. «redacted:github_token».
func Marker(kind string) string { return "«redacted:" + kind + "»" }

// Text redacts known secret formats.
func (r *Redactor) Text(s string) string {
	if s == "" {
		return s
	}
	for _, rule := range Builtin() {
		s = apply(rule, s)
	}
	for _, rule := range r.custom {
		s = apply(rule, s)
	}
	return s
}

// Command redacts known formats and also high-entropy strings, which in
// shell commands are usually inline credentials.
func (r *Redactor) Command(s string) string {
	s = r.Text(s)
	return entropyCandidate().ReplaceAllStringFunc(s, func(tok string) string {
		if strings.Contains(tok, "redacted:") || entropy(tok) <= 4.0 {
			return tok
		}
		return Marker("high_entropy")
	})
}

func apply(rule Rule, s string) string {
	idx := rule.Pattern.SubexpIndex("secret")
	if idx < 0 {
		return rule.Pattern.ReplaceAllLiteralString(s, Marker(rule.Kind))
	}
	return rule.Pattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := rule.Pattern.FindStringSubmatchIndex(m)
		if sub == nil || sub[2*idx] < 0 {
			return m
		}
		secret := m[sub[2*idx]:sub[2*idx+1]]
		if strings.HasPrefix(secret, "«redacted:") {
			return m // already handled by a more specific rule
		}
		return m[:sub[2*idx]] + Marker(rule.Kind) + m[sub[2*idx+1]:]
	})
}

// entropy is the Shannon entropy in bits per character. Hex strings (git
// SHAs, hashes) can't exceed 4.0, so they're never treated as secrets.
func entropy(s string) float64 {
	var counts [256]int
	for i := 0; i < len(s); i++ {
		counts[s[i]]++
	}
	var h float64
	n := float64(len(s))
	for _, c := range counts {
		if c == 0 {
			continue
		}
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h
}

// SecretFile reports whether a path is a file whose contents must never be
// stored (only its path).
func SecretFile(path string) bool {
	base := path
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		base = path[i+1:]
	}
	b := strings.ToLower(base)
	return strings.HasPrefix(b, ".env") || strings.HasSuffix(b, ".pem") || strings.HasSuffix(b, ".key") ||
		strings.HasPrefix(b, "id_rsa") || strings.HasPrefix(b, "id_ed25519") || strings.HasPrefix(b, "id_ecdsa") ||
		strings.HasPrefix(b, "credentials") || b == ".netrc" || b == ".npmrc" || b == ".pypirc"
}
