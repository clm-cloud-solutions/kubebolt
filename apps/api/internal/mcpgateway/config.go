package mcpgateway

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"github.com/kubebolt/kubebolt/apps/api/internal/mcpcatalog"
	"github.com/kubebolt/kubebolt/apps/api/internal/settings"
)

// effective is a stored configuration with an optional patch (a PUT) or draft
// (a test) applied, seen through one auth mode's schema. It is what gets
// validated, what a binder receives and what a PUT writes back.
type effective struct {
	mode mcpcatalog.AuthMode
	// fields holds the non-secret values as sent: the stored ones the schema
	// knows plus every name the patch sets. A name outside the schema stays
	// in so validation rejects it instead of it being dropped silently.
	fields map[string]string
	// secrets holds the readable plaintext after the patch, schema names only.
	secrets map[string]string
	// newSecrets holds only what the patch introduces, so validation checks
	// new values without anything stored being handed back.
	newSecrets map[string]string
	// unreadable lists the schema's stored secrets that no longer decrypt and
	// that the patch neither replaces nor clears.
	unreadable map[string]bool
	// staleFields / staleSecrets are stored names outside the schema (another
	// mode's, or a retired field). A PUT clears them. A generated field is
	// never stale: it belongs to the org, not to the mode that uses it, and
	// switching modes and back must find the value the admin copied.
	staleFields  []string
	staleSecrets []string
}

// effectiveOf applies a keep/clear/set patch (nil keeps, "" clears) over a
// stored configuration for one of def's modes. The stored cfg is not
// modified: settings hands out deep copies, but the gateway does not rely on
// it.
func effectiveOf(def *mcpcatalog.ConnectorDef, mode mcpcatalog.AuthMode, cfg settings.ResolvedConnector, fields, secrets map[string]*string) effective {
	schemaField := map[string]bool{}
	schemaSecret := map[string]bool{}
	for _, f := range mode.Fields {
		if f.Secret {
			schemaSecret[f.Name] = true
		} else {
			schemaField[f.Name] = true
		}
	}
	e := effective{
		mode:       mode,
		fields:     map[string]string{},
		secrets:    map[string]string{},
		newSecrets: map[string]string{},
		unreadable: map[string]bool{},
	}
	generated := generatedFields(def)
	for name, v := range cfg.Fields {
		switch {
		case schemaField[name]:
			e.fields[name] = v
		case !generated[name]:
			e.staleFields = append(e.staleFields, name)
		}
	}
	for name, v := range cfg.Secrets {
		if schemaSecret[name] {
			e.secrets[name] = v
		} else {
			e.staleSecrets = append(e.staleSecrets, name)
		}
	}
	for _, name := range cfg.SecretsUnreadable {
		if schemaSecret[name] {
			e.unreadable[name] = true
		} else {
			e.staleSecrets = append(e.staleSecrets, name)
		}
	}
	for name, v := range fields {
		switch {
		case v == nil:
		case *v == "":
			delete(e.fields, name)
		default:
			e.fields[name] = *v
		}
	}
	for name, v := range secrets {
		if v == nil {
			continue
		}
		delete(e.unreadable, name)
		if *v == "" {
			delete(e.secrets, name)
			continue
		}
		e.newSecrets[name] = *v
		if schemaSecret[name] {
			e.secrets[name] = *v
		}
	}
	return e
}

// validate checks the effective configuration against the mode's schema.
// complete=false relaxes "required" (a disabled connector may be saved half
// filled in) but still checks every value that is present.
func (e effective) validate(complete bool) *settings.ValidationError {
	if complete {
		for _, f := range e.mode.Fields {
			if f.Secret && f.Required && e.unreadable[f.Name] {
				return &settings.ValidationError{
					Field:   f.Name,
					Message: "The saved " + f.Label + " can no longer be decrypted (the server's encryption key changed). Enter it again.",
				}
			}
		}
	}
	mode := e.mode
	if !complete {
		mode.Fields = make([]mcpcatalog.FieldSchema, len(e.mode.Fields))
		copy(mode.Fields, e.mode.Fields)
		for i := range mode.Fields {
			mode.Fields[i].Required = false
		}
	}
	set := make(map[string]bool, len(e.secrets))
	for name := range e.secrets {
		set[name] = true
	}
	if err := mcpcatalog.ValidateConfig(mode, e.fields, set, e.newSecrets); err != nil {
		var fe *mcpcatalog.FieldError
		if errors.As(err, &fe) {
			return &settings.ValidationError{Field: fe.Field, Message: fe.Message}
		}
		return &settings.ValidationError{Field: "config", Message: "The configuration is not valid."}
	}
	return nil
}

// validateWith runs the schema validation and then the binder's own checks,
// when it has any. Without this a multi-tenant admin could save a GCP
// workload-identity config that reads credentials from KubeBolt's own host:
// Bind refuses it later, but the admin would only find out on first use.
func (e effective) validateWith(def *mcpcatalog.ConnectorDef, env mcpcatalog.Env, complete bool) *settings.ValidationError {
	if verr := e.validate(complete); verr != nil {
		return verr
	}
	v, ok := def.Binder.(mcpcatalog.ConfigValidator)
	if !ok {
		return nil
	}
	if err := v.ValidateConfig(e.mode.ID, e.bindFields(), e.newSecrets, env); err != nil {
		var fe *mcpcatalog.FieldError
		if errors.As(err, &fe) {
			return &settings.ValidationError{Field: fe.Field, Message: fe.Message}
		}
		return &settings.ValidationError{Field: "config", Message: "The configuration is not valid."}
	}
	return nil
}

// bindFields is what a binder receives: schema fields with a value, trimmed.
// Validation already refused control characters; surrounding blanks from a
// paste would otherwise end up inside an ARN or a region.
func (e effective) bindFields() map[string]string {
	out := make(map[string]string, len(e.fields))
	for _, f := range e.mode.Fields {
		if f.Secret {
			continue
		}
		if v := strings.TrimSpace(e.fields[f.Name]); v != "" {
			out[f.Name] = v
		}
	}
	return out
}

// generatedFields names the fields KubeBolt generates for a connector, across
// all its modes.
func generatedFields(def *mcpcatalog.ConnectorDef) map[string]bool {
	var out map[string]bool
	for _, m := range def.AuthModes {
		for _, f := range m.Fields {
			if !f.Generated {
				continue
			}
			if out == nil {
				out = map[string]bool{}
			}
			out[f.Name] = true
		}
	}
	return out
}

// refuseGenerated rejects a patch or a draft that tries to set, or clear, a
// generated field: the value exists to be one nobody chose, the AWS external
// ID in particular (the confused deputy). nil (keep) is fine.
func refuseGenerated(def *mcpcatalog.ConnectorDef, patches ...map[string]*string) *settings.ValidationError {
	generated := generatedFields(def)
	if len(generated) == 0 {
		return nil
	}
	var names []string
	for _, p := range patches {
		for name, v := range p {
			if v != nil && generated[name] {
				names = append(names, name)
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return &settings.ValidationError{Field: names[0], Message: fieldLabel(def, names[0]) + " is generated by KubeBolt; it cannot be set."}
}

// fieldLabel is the label a field carries in any of def's modes, or its name.
func fieldLabel(def *mcpcatalog.ConnectorDef, name string) string {
	for _, m := range def.AuthModes {
		for _, f := range m.Fields {
			if f.Name == name && f.Label != "" {
				return f.Label
			}
		}
	}
	return name
}

// newGeneratedValue is a value nobody can guess or choose: 32 random bytes,
// hex-encoded. Hex fits every generated field's pattern (STS's ExternalId
// charset included).
func newGeneratedValue() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// bindSecrets is the schema's readable secrets.
func (e effective) bindSecrets() map[string]string {
	out := make(map[string]string, len(e.secrets))
	for name, v := range e.secrets {
		out[name] = v
	}
	return out
}
