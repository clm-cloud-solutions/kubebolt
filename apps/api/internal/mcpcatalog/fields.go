package mcpcatalog

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// FieldError names the field an admin has to fix. The API maps it to
// 400 {error:"validation failed", field, message}, which the drawer shows
// under the offending input.
type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

const defaultFieldMaxLen = 256

var (
	patternCacheMu sync.Mutex
	patternCache   = map[string]*regexp.Regexp{}
)

func compiledPattern(p string) *regexp.Regexp {
	patternCacheMu.Lock()
	defer patternCacheMu.Unlock()
	if re, ok := patternCache[p]; ok {
		return re
	}
	re := regexp.MustCompile(p)
	patternCache[p] = re
	return re
}

// ValidateConfig checks an org's configuration for one auth mode before it
// is stored or used. fields are the non-secret values; secretsSet reports,
// per secret field, whether a value will exist after the write (a stored
// value that is being kept counts); secretValues holds only the NEW
// plaintext values of this write, so their format can be checked without the
// caller ever handing back what is already stored.
//
// Unknown fields are rejected: a field the schema does not know is either a
// typo or an attempt to smuggle something into a binder.
func ValidateConfig(mode AuthMode, fields map[string]string, secretsSet map[string]bool, secretValues map[string]string) error {
	known := make(map[string]FieldSchema, len(mode.Fields))
	for _, f := range mode.Fields {
		known[f.Name] = f
	}
	for name := range fields {
		f, ok := known[name]
		if !ok || f.Secret {
			return &FieldError{Field: name, Message: "is not a setting of this connector"}
		}
	}
	for name := range secretValues {
		f, ok := known[name]
		if !ok || !f.Secret {
			return &FieldError{Field: name, Message: "is not a secret of this connector"}
		}
	}
	for _, f := range mode.Fields {
		var (
			raw     string
			present bool
		)
		if f.Secret {
			present = secretsSet[f.Name]
			raw = secretValues[f.Name]
		} else {
			raw = fields[f.Name]
			present = strings.TrimSpace(raw) != ""
		}
		// Control characters are checked on the raw value: trimming first
		// would let a trailing CR through, and a CR/LF in a value that ends
		// up in a header or a URL is exactly the injection this guards.
		forbidden := "\x00\r\n"
		if f.Multiline {
			forbidden = "\x00"
		}
		if strings.ContainsAny(raw, forbidden) {
			return &FieldError{Field: f.Name, Message: f.Label + " contains invalid characters"}
		}
		value := strings.TrimSpace(raw)
		if f.Required && !present {
			return &FieldError{Field: f.Name, Message: f.Label + " is required"}
		}
		if value == "" {
			continue
		}
		max := f.MaxLen
		if max == 0 {
			max = defaultFieldMaxLen
		}
		if len(value) > max {
			return &FieldError{Field: f.Name, Message: fmt.Sprintf("%s is too long (max %d characters)", f.Label, max)}
		}
		if !utf8.ValidString(value) {
			return &FieldError{Field: f.Name, Message: f.Label + " contains invalid characters"}
		}
		if len(f.Options) > 0 {
			valid := false
			for _, o := range f.Options {
				if o.Value == value {
					valid = true
					break
				}
			}
			if !valid {
				return &FieldError{Field: f.Name, Message: f.Label + " is not one of the allowed values"}
			}
		}
		if f.Pattern != "" && !compiledPattern(f.Pattern).MatchString(value) {
			return &FieldError{Field: f.Name, Message: f.Label + " is not in the expected format"}
		}
	}
	return nil
}
