package userdefined

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// A manifest declares a tool's parameters as a JSON Schema object, the same
// way agi's own tools do, and that object is handed to the provider as it was
// written. It is also what a call is checked against before the script is run,
// which is the reason only part of JSON Schema is accepted: agi will not
// advertise a constraint it does not enforce, and a tool whose arguments were
// checked by nobody would have every script parsing and re-checking the same
// JSON that its own manifest already described.
//
// So the accepted keywords are the ones below and no others. A manifest using
// anything else is refused when it is read, with the keyword named, rather
// than being accepted and quietly unchecked.
var supportedKeywords = map[string]bool{
	"type":                 true,
	"description":          true,
	"properties":           true,
	"required":             true,
	"items":                true,
	"enum":                 true,
	"additionalProperties": true,
}

// The JSON types a parameter may declare. They are JSON Schema's own spellings
// rather than agi's, since what the manifest writes here is JSON Schema and
// goes to the provider unchanged.
const (
	typeString  = "string"
	typeNumber  = "number"
	typeInteger = "integer"
	typeBoolean = "boolean"
	typeArray   = "array"
	typeObject  = "object"
)

var declarableTypes = map[string]bool{
	typeString:  true,
	typeNumber:  true,
	typeInteger: true,
	typeBoolean: true,
	typeArray:   true,
	typeObject:  true,
}

// rule is one declared value in the checked form of a schema.
type rule struct {
	kind string
	// enum, when set, is the whole of what the value may be.
	enum []any
	// items describes an array's elements. Nil means an array whose elements
	// were not described, and are therefore not checked.
	items *rule
	// object describes an object's properties, for a nested one.
	object *objectRule
}

// objectRule is a whole object's worth of declarations: what its properties
// are, which of them a call must carry, and whether anything else may be
// passed alongside them.
type objectRule struct {
	// names are the declared properties, sorted, so that a call with several
	// problems reports them in the same order every time.
	names    []string
	props    map[string]*rule
	required []string
	// extras says whether properties the schema did not declare are allowed.
	// It follows JSON Schema, where they are unless additionalProperties says
	// otherwise: the same object is sent to the provider, and the two must not
	// disagree about what it means.
	extras bool
}

// emptyParams is the schema given to a tool that declared no parameters. A
// provider is told the tool takes an object with nothing in it, which is true,
// rather than being told nothing at all.
func emptyParams() map[string]any {
	return map[string]any{
		"type":                 typeObject,
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
}

// compileParams turns a manifest's parameters into the schema the model is
// shown and the rule a call is checked against. A tool that declared none gets
// an empty object schema, which is the honest description of a tool that takes
// nothing.
func compileParams(raw map[string]any) (map[string]any, *objectRule, error) {
	if len(raw) == 0 {
		params := emptyParams()
		check, err := compileObject(params, "")
		return params, check, err
	}
	check, err := compileObject(raw, "")
	if err != nil {
		return nil, nil, err
	}
	return raw, check, nil
}

// compileObject checks an object schema and returns the rule for it. The path
// is where in the parameters this object sits, empty at the top, so a mistake
// deep in a nested schema can be pointed at.
func compileObject(raw map[string]any, path string) (*objectRule, error) {
	if err := checkKeywords(raw, path); err != nil {
		return nil, err
	}
	if kind, _ := raw["type"].(string); kind != typeObject {
		return nil, fmt.Errorf("%s must be declared as %q", at(path, "the parameters"), typeObject)
	}

	declared, err := properties(raw, path)
	if err != nil {
		return nil, err
	}

	o := &objectRule{props: make(map[string]*rule, len(declared)), extras: true}
	for name, spec := range declared {
		r, err := compileRule(spec, join(path, name))
		if err != nil {
			return nil, err
		}
		o.props[name] = r
		o.names = append(o.names, name)
	}
	sort.Strings(o.names)

	if o.required, err = requiredNames(raw, o, path); err != nil {
		return nil, err
	}
	if extras, present := raw["additionalProperties"]; present {
		allowed, ok := extras.(bool)
		if !ok {
			return nil, fmt.Errorf("%s declares additionalProperties as something other than "+
				"true or false; agi checks a call against the properties a schema names, and a "+
				"schema for the rest of them is not something it can enforce",
				at(path, "the parameters"))
		}
		o.extras = allowed
	}
	return o, nil
}

// properties returns the declared properties of an object schema, which may be
// none at all: a tool that takes nothing still declares that it takes nothing.
func properties(raw map[string]any, path string) (map[string]map[string]any, error) {
	spec, present := raw["properties"]
	if !present {
		return nil, nil
	}
	object, ok := spec.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s declares properties that are not a JSON object", at(path, "the parameters"))
	}
	out := make(map[string]map[string]any, len(object))
	for name, value := range object {
		declaration, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("the parameter %s is not described by a JSON object", join(path, name))
		}
		out[name] = declaration
	}
	return out, nil
}

// requiredNames reads the required list, refusing a name the schema never
// declared: a call could not satisfy it, so the manifest is wrong rather than
// every call being.
func requiredNames(raw map[string]any, o *objectRule, path string) ([]string, error) {
	spec, present := raw["required"]
	if !present {
		return nil, nil
	}
	list, ok := spec.([]any)
	if !ok {
		return nil, fmt.Errorf("%s declares required as something other than a list of names", at(path, "the parameters"))
	}
	names := make([]string, 0, len(list))
	for _, value := range list {
		name, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s lists something other than a name in required", at(path, "the parameters"))
		}
		if _, declared := o.props[name]; !declared {
			return nil, fmt.Errorf("%s requires the parameter %q, which it does not declare",
				at(path, "the parameters"), name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// compileRule checks one parameter's declaration.
func compileRule(raw map[string]any, path string) (*rule, error) {
	if err := checkKeywords(raw, path); err != nil {
		return nil, err
	}

	kind, ok := raw["type"].(string)
	if !ok || kind == "" {
		return nil, fmt.Errorf("the parameter %s declares no type; agi checks a call against "+
			"the types a schema names, so every parameter needs one", path)
	}
	if !declarableTypes[kind] {
		return nil, fmt.Errorf("the parameter %s is declared as type %q, which is not one of %s",
			path, kind, strings.Join(sortedKeys(declarableTypes), ", "))
	}

	r := &rule{kind: kind}
	if spec, present := raw["enum"]; present {
		values, ok := spec.([]any)
		if !ok || len(values) == 0 {
			return nil, fmt.Errorf("the parameter %s declares an enum that is not a non-empty list", path)
		}
		r.enum = values
	}

	switch kind {
	case typeObject:
		nested, err := compileObject(raw, path)
		if err != nil {
			return nil, err
		}
		r.object = nested
	case typeArray:
		spec, present := raw["items"]
		if !present {
			break
		}
		declaration, ok := spec.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("the parameter %s describes its items with something other than a JSON object", path)
		}
		items, err := compileRule(declaration, path+"[]")
		if err != nil {
			return nil, err
		}
		r.items = items
	}
	return r, nil
}

// checkKeywords refuses a declaration using a keyword agi does not enforce.
// Accepting one would advertise a constraint to the model that nothing checks,
// which is worse than not offering it: the model would be told a call was
// valid in a way agi never verified.
func checkKeywords(raw map[string]any, path string) error {
	for _, keyword := range sortedKeys(mapKeys(raw)) {
		if supportedKeywords[keyword] {
			continue
		}
		return fmt.Errorf("%s uses the JSON Schema keyword %q, which agi does not check; "+
			"the keywords it does are %s", at(path, "the parameters"), keyword,
			strings.Join(sortedKeys(supportedKeywords), ", "))
	}
	return nil
}

// InvalidArguments reports every way one call's arguments failed to match the
// parameters, in a single error, so that a model correcting the call is told
// all of it at once rather than one problem per attempt.
type InvalidArguments struct {
	Problems []string
}

// Error implements error.
func (e *InvalidArguments) Error() string {
	return "the arguments do not match the tool's parameters: " + strings.Join(e.Problems, "; ")
}

// validate checks the model's arguments and returns the JSON to hand the
// script: the arguments as they were written, or an empty object when the
// model sent nothing, so that a script always parses an object.
//
// The arguments are not rewritten. What a script receives is what the model
// produced, because a tool reading its own parameters should see the call that
// was actually made.
func (o *objectRule) validate(args json.RawMessage) (string, error) {
	text := strings.TrimSpace(string(args))
	if text == "" {
		text = "{}"
	}

	dec := json.NewDecoder(strings.NewReader(text))
	// Numbers stay as written, so an integer can be told from a decimal.
	dec.UseNumber()

	var document any
	if err := dec.Decode(&document); err != nil {
		return "", fmt.Errorf("the arguments are not valid JSON: %w", err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return "", errors.New("the arguments are not valid JSON: more than one value")
	}
	object, ok := document.(map[string]any)
	if !ok {
		return "", fmt.Errorf("the arguments are a %s, not a JSON object of named parameters", jsonKind(document))
	}

	if problems := o.check(object, ""); len(problems) > 0 {
		return "", &InvalidArguments{Problems: problems}
	}
	return compactJSON(text), nil
}

// check reports every way one object fails to match the declarations: a
// required parameter missing, a value of the wrong type, or — when the schema
// says so — a parameter that was never declared.
func (o *objectRule) check(object map[string]any, path string) []string {
	var problems []string
	for _, name := range o.required {
		if value, present := object[name]; !present || value == nil {
			problems = append(problems, fmt.Sprintf("%s is required", join(path, name)))
		}
	}
	for _, name := range o.names {
		value, present := object[name]
		// An absent parameter is either required, and already reported, or
		// optional, and nothing to report. Null is read as absent for the
		// same reason: it is how a model leaves an optional value out.
		if !present || value == nil {
			continue
		}
		problems = append(problems, o.props[name].check(value, join(path, name))...)
	}
	if !o.extras {
		var unknown []string
		for name := range object {
			if _, declared := o.props[name]; !declared {
				unknown = append(unknown, join(path, name))
			}
		}
		sort.Strings(unknown)
		for _, name := range unknown {
			problems = append(problems, fmt.Sprintf("%s is not a parameter of this tool", name))
		}
	}
	return problems
}

// check reports what is wrong with one value, if anything.
func (r *rule) check(value any, path string) []string {
	switch r.kind {
	case typeString:
		if _, ok := value.(string); !ok {
			return []string{wrongType(path, "a string", value)}
		}
	case typeBoolean:
		if _, ok := value.(bool); !ok {
			return []string{wrongType(path, "true or false", value)}
		}
	case typeNumber:
		if _, ok := value.(json.Number); !ok {
			return []string{wrongType(path, "a number", value)}
		}
	case typeInteger:
		number, ok := value.(json.Number)
		if !ok {
			return []string{wrongType(path, "a whole number", value)}
		}
		// 3.0 is how a decimal is written, so it is not an integer here even
		// though it names one.
		if _, err := number.Int64(); err != nil {
			return []string{fmt.Sprintf("%s must be a whole number, got %s", path, number)}
		}
	case typeArray:
		items, ok := value.([]any)
		if !ok {
			return []string{wrongType(path, "a list", value)}
		}
		if r.items == nil {
			break
		}
		var problems []string
		for i, item := range items {
			problems = append(problems, r.items.check(item, fmt.Sprintf("%s[%d]", path, i))...)
		}
		return problems
	case typeObject:
		object, ok := value.(map[string]any)
		if !ok {
			return []string{wrongType(path, "an object", value)}
		}
		if r.object == nil {
			break
		}
		return r.object.check(object, path)
	}

	if len(r.enum) > 0 && !within(r.enum, value) {
		return []string{fmt.Sprintf("%s must be one of %s, got %s", path, choices(r.enum), literal(value))}
	}
	return nil
}

// within reports whether a value is one of the declared choices, comparing
// them as they are written: an enum is read out of the manifest as ordinary
// JSON, and a value out of a call as a json.Number, so the two are compared
// through the text of each rather than by Go's idea of equality.
func within(enum []any, value any) bool {
	want := literal(value)
	for _, choice := range enum {
		if literal(choice) == want {
			return true
		}
	}
	return false
}

// choices renders the accepted values of an enum for a correction.
func choices(enum []any) string {
	out := make([]string, 0, len(enum))
	for _, choice := range enum {
		out = append(out, literal(choice))
	}
	return strings.Join(out, ", ")
}

// literal renders a scalar the way it was written, for comparing and for
// quoting back in a correction.
func literal(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		// How a number read without UseNumber arrives, which is the case for
		// an enum taken from the manifest.
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%f", v), "0"), ".")
	case bool:
		return fmt.Sprintf("%t", v)
	case nil:
		return "null"
	default:
		return jsonKind(value)
	}
}

// wrongType phrases the commonest correction there is.
func wrongType(path, want string, got any) string {
	return fmt.Sprintf("%s must be %s, got %s", path, want, jsonKind(got))
}

// jsonKind names what a decoded value turned out to be, in the vocabulary of
// JSON rather than of Go: the model wrote JSON and reads the correction.
func jsonKind(value any) string {
	switch value.(type) {
	case string:
		return "a string"
	case json.Number, float64:
		return "a number"
	case bool:
		return "a boolean"
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	case nil:
		return "null"
	default:
		return "something else"
	}
}

// join names a property within its parent, so a problem in a nested object
// reads as the path to the value it is about.
func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// at names where in the parameters something is, for a manifest that could not
// be read. The top of the schema has no path of its own, so it is named by
// what it is.
func at(path, top string) string {
	if path == "" {
		return top
	}
	return "the parameter " + path
}

// compactJSON puts arguments on one line, leaving them alone if they cannot be
// compacted. They have already been parsed by the time this is called, so the
// fallback is for safety rather than for a case that happens.
func compactJSON(text string) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(text)); err != nil {
		return text
	}
	return buf.String()
}

func mapKeys(m map[string]any) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
