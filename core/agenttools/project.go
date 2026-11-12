package agenttools

import (
	"maps"
	"regexp"
	"strings"
	"unicode"
)

// DeniedFields are the keys the projection drops, at every depth, from every
// result an agent surface returns. A key is denied when, read as snake case,
// it is one of these, a plural of one, or ends in one after an underscore
// (access_token, current_password), or, for the two that are several words,
// contains it (private_key_pem). That errs toward dropping: a field this
// removes that was harmless costs a column, and one it kept that was not would
// cost a key.
var DeniedFields = []string{
	"recipient", "identity", "bundle", "password", "secret",
	"token", "private_key", "credential", "passphrase", "key_material",
}

// The shapes of key material on a page. They are shared with
// core/tests/encryptedretention, whose detector reads these rather than a copy,
// so a shape added for one is looked for by both.
//
// The bare prefix is not enough for an identity: the refusal for a secret that
// is not an identity quotes "AGE-SECRET-KEY-1" as what it expected, and that
// sentence is allowed.
var (
	AgeIdentityShape   = regexp.MustCompile(`AGE-SECRET-KEY-(PQ-)?1[A-Z0-9]{20,}`)
	AgeRecipientShape  = regexp.MustCompile(`age1(?:pq1)?[023456789acdefghjklmnpqrstuvwxyz]{20,}`)
	PEMPrivateKeyShape = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----`)
	CanaryShapes       = []*regexp.Regexp{AgeIdentityShape, AgeRecipientShape, PEMPrivateKeyShape}
)

// Withheld replaces a string that carries one of the canary shapes. It is a
// fixed marker, so the result says something was removed and never what.
const Withheld = "[withheld]"

// Project walks a decoded JSON value and returns a copy with every denied key,
// every ssh_key_id and the source of every key_provider object removed at every depth and every string that carries a canary shape replaced
// by Withheld. It is the one projection a tool result passes through at the
// tool boundary, over a result as decoded from the API and before anything
// else sees it. It never changes its argument, and a value that is not a
// map, a slice or a string comes back as it is.
func Project(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if deniedField(k) || snake(k) == "ssh_key_id" {
				continue
			}
			// Where a key is kept: the provider's source is a path, a variable
			// name or a command, so it goes whatever the object holding it is.
			if m, ok := val.(map[string]any); ok && strings.HasSuffix(snake(k), "key_provider") {
				m = maps.Clone(m)
				delete(m, "source")
				val = m
			}
			out[k] = Project(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = Project(val)
		}
		return out
	case string:
		for _, shape := range CanaryShapes {
			if shape.MatchString(x) {
				return Withheld
			}
		}
		return x
	default:
		return v
	}
}

func deniedField(key string) bool {
	k := snake(key)
	for _, f := range DeniedFields {
		if k == f || k == f+"s" || strings.HasSuffix(k, "_"+f) || strings.HasSuffix(k, "_"+f+"s") {
			return true
		}
		if strings.Contains(f, "_") && strings.Contains(k, f) {
			return true
		}
	}
	return false
}

// snake lowercases a key and puts an underscore at each camel hump and in
// place of a hyphen, so currentPassword and current-password read as
// current_password.
func snake(key string) string {
	var b strings.Builder
	for i, r := range key {
		switch {
		case r == '-':
			b.WriteByte('_')
		case unicode.IsUpper(r):
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
