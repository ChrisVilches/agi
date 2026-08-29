package userdefined

import (
	"encoding/json"
	"strings"
	"testing"
)

// compile turns a JSON schema written as text into the rule a call is checked
// against, failing the test if the schema itself is refused.
func compile(t *testing.T, schema string) *objectRule {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(schema), &raw); err != nil {
		t.Fatalf("the test's own schema is not JSON: %v", err)
	}
	_, check, err := compileParams(raw)
	if err != nil {
		t.Fatalf("compileParams() = %v", err)
	}
	return check
}

// refuse returns the error compiling a schema produced, failing the test if it
// produced none.
func refuse(t *testing.T, schema string) string {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(schema), &raw); err != nil {
		t.Fatalf("the test's own schema is not JSON: %v", err)
	}
	if _, _, err := compileParams(raw); err != nil {
		return err.Error()
	}
	t.Fatalf("compileParams() accepted %s, want an error", schema)
	return ""
}

// A tool that takes nothing is described as taking an object with nothing in
// it, which is true, rather than being described not at all.
func TestParametersDefaultToAnEmptyObject(t *testing.T) {
	params, check, err := compileParams(nil)
	if err != nil {
		t.Fatalf("compileParams(nil) = %v", err)
	}
	if params["type"] != "object" {
		t.Errorf("compileParams(nil) = %v, want an object schema", params)
	}
	if _, err := check.validate(nil); err != nil {
		t.Errorf("validate() of no arguments = %v, want it accepted", err)
	}
	if _, err := check.validate(json.RawMessage(`{"city": "Lima"}`)); err == nil {
		t.Error("validate() accepted a parameter a tool taking nothing cannot have")
	}
}

// agi will not advertise a constraint it does not enforce, so a schema it
// cannot check is refused when the manifest is read.
func TestUnsupportedSchemasAreRefusedUpFront(t *testing.T) {
	cases := []struct{ name, schema, want string }{
		{
			name:   "a keyword agi does not check",
			schema: `{"type": "object", "properties": {"n": {"type": "integer", "minimum": 1}}}`,
			want:   "minimum",
		},
		{
			name:   "a parameter with no type",
			schema: `{"type": "object", "properties": {"n": {"description": "a number"}}}`,
			want:   "declares no type",
		},
		{
			name:   "a type that is not a JSON type",
			schema: `{"type": "object", "properties": {"n": {"type": "date"}}}`,
			want:   `"date"`,
		},
		{
			name:   "a top level that is not an object",
			schema: `{"type": "array", "items": {"type": "string"}}`,
			want:   "object",
		},
		{
			name:   "requiring something never declared",
			schema: `{"type": "object", "properties": {"a": {"type": "string"}}, "required": ["b"]}`,
			want:   `"b"`,
		},
		{
			name:   "a schema where a yes or no belongs",
			schema: `{"type": "object", "properties": {}, "additionalProperties": {"type": "string"}}`,
			want:   "additionalProperties",
		},
		{
			name:   "properties that are not declarations",
			schema: `{"type": "object", "properties": {"a": "string"}}`,
			want:   "not described by a JSON object",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := refuse(t, c.schema); !strings.Contains(got, c.want) {
				t.Errorf("compileParams() = %q, want it to mention %q", got, c.want)
			}
		})
	}
}

func TestValidateChecksTypes(t *testing.T) {
	check := compile(t, `{
		"type": "object",
		"properties": {
			"city": {"type": "string"},
			"days": {"type": "integer"},
			"rate": {"type": "number"},
			"dry":  {"type": "boolean"}
		},
		"required": ["city"]
	}`)

	good := []string{
		`{"city": "Lima"}`,
		`{"city": "Lima", "days": 3, "rate": 1.5, "dry": true}`,
		`{"city": "Lima", "rate": 2}`,
		// An optional parameter left out as null is left out.
		`{"city": "Lima", "days": null}`,
	}
	for _, args := range good {
		if _, err := check.validate(json.RawMessage(args)); err != nil {
			t.Errorf("validate(%s) = %v, want it accepted", args, err)
		}
	}

	bad := []struct{ args, want string }{
		{`{}`, "city is required"},
		{`{"city": null}`, "city is required"},
		{`{"city": 5}`, "city must be a string, got a number"},
		{`{"city": "Lima", "days": "three"}`, "days must be a whole number, got a string"},
		// 3.0 is how a decimal is written, so it is not a whole number here.
		{`{"city": "Lima", "days": 3.0}`, "days must be a whole number, got 3.0"},
		{`{"city": "Lima", "dry": "yes"}`, "dry must be true or false"},
		{`{"city": "Lima", "rate": "fast"}`, "rate must be a number"},
	}
	for _, c := range bad {
		_, err := check.validate(json.RawMessage(c.args))
		if err == nil {
			t.Errorf("validate(%s) = nil, want %q", c.args, c.want)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("validate(%s) = %v, want it to say %q", c.args, err, c.want)
		}
	}
}

// Everything wrong with one call is reported at once, so a correction can fix
// all of it rather than one problem per attempt.
func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	check := compile(t, `{
		"type": "object",
		"properties": {"city": {"type": "string"}, "days": {"type": "integer"}},
		"required": ["city", "days"],
		"additionalProperties": false
	}`)

	_, err := check.validate(json.RawMessage(`{"days": "three", "colour": "red"}`))
	if err == nil {
		t.Fatal("validate() = nil, want an error")
	}
	for _, want := range []string{"city is required", "days must be a whole number", "colour is not a parameter"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("validate() = %v, want it to say %q", err, want)
		}
	}
}

// additionalProperties means what JSON Schema says it means, because the same
// object is what the provider is shown: the two must not disagree about it.
func TestValidateFollowsAdditionalProperties(t *testing.T) {
	open := compile(t, `{"type": "object", "properties": {"a": {"type": "string"}}}`)
	if _, err := open.validate(json.RawMessage(`{"a": "x", "b": "y"}`)); err != nil {
		t.Errorf("validate() = %v, want extras allowed where the schema does not forbid them", err)
	}

	closed := compile(t, `{"type": "object", "properties": {"a": {"type": "string"}}, "additionalProperties": false}`)
	if _, err := closed.validate(json.RawMessage(`{"a": "x", "b": "y"}`)); err == nil {
		t.Error("validate() = nil, want the undeclared parameter refused")
	}
}

func TestValidateChecksEnums(t *testing.T) {
	check := compile(t, `{
		"type": "object",
		"properties": {"unit": {"type": "string", "enum": ["celsius", "fahrenheit"]}}
	}`)

	if _, err := check.validate(json.RawMessage(`{"unit": "celsius"}`)); err != nil {
		t.Errorf("validate() = %v, want a declared choice accepted", err)
	}
	_, err := check.validate(json.RawMessage(`{"unit": "kelvin"}`))
	if err == nil {
		t.Fatal("validate() = nil, want an undeclared choice refused")
	}
	if !strings.Contains(err.Error(), "celsius, fahrenheit") {
		t.Errorf("validate() = %v, want the choices listed", err)
	}
}

func TestValidateChecksListsAndNestedObjects(t *testing.T) {
	check := compile(t, `{
		"type": "object",
		"properties": {
			"names": {"type": "array", "items": {"type": "string"}},
			"anything": {"type": "array"},
			"where": {
				"type": "object",
				"properties": {"city": {"type": "string"}},
				"required": ["city"],
				"additionalProperties": false
			}
		}
	}`)

	if _, err := check.validate(json.RawMessage(`{"names": ["a", "b"], "anything": [1, "x"], "where": {"city": "Lima"}}`)); err != nil {
		t.Errorf("validate() = %v, want it accepted", err)
	}

	bad := []struct{ args, want string }{
		{`{"names": "a"}`, "names must be a list"},
		{`{"names": ["a", 2]}`, "names[1] must be a string"},
		{`{"where": {"city": 1}}`, "where.city must be a string"},
		{`{"where": {}}`, "where.city is required"},
		{`{"where": {"city": "Lima", "country": "PE"}}`, "where.country is not a parameter"},
		{`{"where": "Lima"}`, "where must be an object"},
	}
	for _, c := range bad {
		_, err := check.validate(json.RawMessage(c.args))
		if err == nil {
			t.Errorf("validate(%s) = nil, want %q", c.args, c.want)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("validate(%s) = %v, want it to say %q", c.args, err, c.want)
		}
	}
}

// What the script is handed is the call the model made, on one line, and a
// call with no arguments still arrives as an object to parse.
func TestValidateReturnsThePayloadToPassOn(t *testing.T) {
	check := compile(t, `{"type": "object", "properties": {"a": {"type": "string"}}}`)

	got, err := check.validate(json.RawMessage("{\n  \"a\": \"x\"\n}"))
	if err != nil {
		t.Fatalf("validate() = %v", err)
	}
	if got != `{"a":"x"}` {
		t.Errorf("validate() = %q, want the arguments compacted", got)
	}

	for _, empty := range []json.RawMessage{nil, json.RawMessage(""), json.RawMessage("   ")} {
		got, err := check.validate(empty)
		if err != nil {
			t.Fatalf("validate(%q) = %v", empty, err)
		}
		if got != "{}" {
			t.Errorf("validate(%q) = %q, want an empty object", empty, got)
		}
	}
}

func TestValidateRefusesArgumentsThatAreNotAnObject(t *testing.T) {
	check := compile(t, `{"type": "object", "properties": {}}`)

	for _, args := range []string{`[1, 2]`, `"text"`, `5`, `{`, `{} {}`} {
		if _, err := check.validate(json.RawMessage(args)); err == nil {
			t.Errorf("validate(%s) = nil, want an error", args)
		}
	}
}
