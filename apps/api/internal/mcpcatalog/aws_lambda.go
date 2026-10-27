package mcpcatalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// A Lambda function's configuration carries its environment variables, and
// teams keep database passwords and API keys there as often as in a
// Kubernetes Secret. GetFunctionConfiguration returns them, and so does a
// CloudTrail UpdateFunctionConfiguration request when AWS does not mask it.
// KubeBolt keeps each variable's name and replaces its value before the model
// sees the answer: a diagnosis needs to know that QUEUE_URL is set, not what
// it holds, and no secret value is ever in reach.

const awsLambdaHiddenValue = "[REDACTED]"

const awsLambdaHiddenNote = "KubeBolt replaced the values of %d environment variables with " + awsLambdaHiddenValue +
	"; their names are kept. Their values are never available through this connector."

// awsIsLambda says whether a call's answer can hold a function's environment.
func awsIsLambda(service string) bool {
	return awsNorm(service) == "lambda"
}

// awsHideEnvironment replaces the value of every environment variable in v,
// wherever an object holds an Environment (or environment) object with a
// Variables map, and returns the re-encoded answer and how many values it
// replaced. An answer that is not JSON is an error: the caller discards it
// rather than show something it could not check.
func awsHideEnvironment(v json.RawMessage) (json.RawMessage, int, error) {
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, 0, fmt.Errorf("mcpcatalog: aws answer is not JSON: %w", err)
	}
	n := awsHideIn(doc)
	if n == 0 {
		return v, 0, nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, 0, err
	}
	return json.RawMessage(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))), n, nil
}

func awsHideIn(node any) int {
	n := 0
	switch t := node.(type) {
	case map[string]any:
		for k, child := range t {
			if strings.EqualFold(k, "Environment") {
				n += awsHideVariables(child)
			}
			n += awsHideIn(child)
		}
	case []any:
		for _, child := range t {
			n += awsHideIn(child)
		}
	}
	return n
}

// awsHideVariables replaces the values of env's Variables map. A value that
// is already a string AWS masked (CloudTrail's HIDDEN_DUE_TO_SECURITY_REASONS)
// is not a map and is left alone.
func awsHideVariables(env any) int {
	m, ok := env.(map[string]any)
	if !ok {
		return 0
	}
	n := 0
	for k, vars := range m {
		if !strings.EqualFold(k, "Variables") {
			continue
		}
		vm, ok := vars.(map[string]any)
		if !ok {
			continue
		}
		for name, val := range vm {
			if val == awsLambdaHiddenValue {
				continue
			}
			vm[name] = awsLambdaHiddenValue
			n++
		}
	}
	return n
}
