package main

import (
	"strings"
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// A ConfigMap value is an arbitrary string: a JSON document, a comma-joined
// list, a sentence. This template wrapped it in hand-written quotes, so the
// first `"` inside a value closed the string early and the remainder became
// YAML — a render that refuses its own output with
//
//	configmap.yaml: yaml: line 6: did not find expected key
//
// and then deletes the tree it wrote, leaving nothing to read. service-go and
// service-go-grpc already rendered `printf "%q"`; this agent and
// service-python-fastapi did not (codefly-dev/service-python-fastapi#79).
//
// The template is executed directly rather than through the agent's deployment
// path, because the fault is the template's quoting and nothing above it.
func TestConfigMapQuotesValuesThatContainYAMLSyntax(t *testing.T) {
	hostile := map[string]string{
		"MODULE_PRINCIPALS":  `{"documents":{"scopes":["read","write"]}}`,
		"COLON_AND_SPACE":    "key: value",
		"TRAILING_BACKSLASH": `C:\path\`,
		"NEWLINE":            "first\nsecond",
		"QUOTED_WORD":        `say "hello"`,
		"HASH":               "value # not a comment",
		"EMPTY":              "",
	}
	raw, err := deploymentFS.ReadFile("templates/deployment/kustomize/overlays/environment/configmap.yaml.tmpl")
	require.NoError(t, err)
	parsed, err := template.New("configmap").Parse(string(raw))
	require.NoError(t, err)

	var out strings.Builder
	require.NoError(t, parsed.Execute(&out, map[string]any{
		"Name":      "frontend",
		"Namespace": "platform-obin-saas",
		"ConfigMap": hostile,
	}))

	var document struct {
		Data map[string]string `yaml:"data"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(out.String()), &document),
		"the rendered configmap is not parseable YAML:\n%s", out.String())
	for key, want := range hostile {
		require.Equal(t, want, document.Data[key], "value for %s did not survive rendering", key)
	}
}
