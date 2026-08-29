package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// A Schema is the shape the user asked the run's final answer to take. It is a
// flat set of named scalar fields — no nesting, no optional fields — which is
// the most a model can be relied on to produce from a prompt alone. The whole
// answer is either one such object or a list of them, and that choice is the
// only nesting there is: a field never holds a list or an object of its own.
//
// Nothing here is sent to a provider as a structured-output request: agi asks
// for the shape in the prompt and checks what comes back, which is the only
// approach that behaves the same behind every gateway.

// FieldType is the type of one schema field. The set is closed to the four
// scalars below.
type FieldType string

const (
	TypeString  FieldType = "string"
	TypeBool    FieldType = "bool"
	TypeInt     FieldType = "int"
	TypeDecimal FieldType = "decimal"
)

// fieldTypes maps every accepted spelling of a type to the type itself. Both
// the short and the long spelling are accepted, so "str" and "string" name the
// same thing.
var fieldTypes = map[string]FieldType{
	"str":     TypeString,
	"string":  TypeString,
	"bool":    TypeBool,
	"boolean": TypeBool,
	"int":     TypeInt,
	"integer": TypeInt,
	"dec":     TypeDecimal,
	"decimal": TypeDecimal,
}

// Field is one named value in a Schema. Description is what the model is told
// the field means, so it is worth writing well; it is optional all the same.
type Field struct {
	Name        string
	Type        FieldType
	Description string
}

// Schema is an ordered set of fields with distinct names. List says whether
// those fields describe the whole answer or one item of a list of answers.
type Schema struct {
	Fields []Field
	// List is set when the spec was wrapped in brackets, meaning the answer is
	// a JSON array of objects with these fields rather than one such object.
	List bool
}

// ParseSchema reads the command-line spelling of a schema: comma-separated
// fields, each written name:type:"description", as in
//
//	id:int:"User ID", name:str:"Full name", active:bool:"Is active"
//
// Wrapping the whole spec in brackets asks for a list of such objects instead
// of one of them, as in
//
//	[id:int:"User ID", name:str:"Full name"]
//
// The brackets go around the whole spec and nowhere else. There is one list at
// most and a field is always a scalar, so neither a list of lists nor a field
// holding a list — names:str[] — can be written.
//
// Commas and colons inside the quoted description are part of it. The
// description may be left off entirely. An empty spec is an error, since a
// schema with no fields constrains nothing.
func ParseSchema(spec string) (*Schema, error) {
	body, list, err := unwrapList(spec)
	if err != nil {
		return nil, fmt.Errorf("model: bad schema %q: %w", spec, err)
	}

	raw, err := splitTop(body, ',')
	if err != nil {
		return nil, fmt.Errorf("model: bad schema %q: %w", spec, err)
	}

	s := Schema{List: list}
	seen := make(map[string]struct{}, len(raw))
	for _, r := range raw {
		// A doubled or trailing comma can only be a typo, and it leaves the
		// meaning of the schema unchanged, so it is ignored.
		if strings.TrimSpace(r) == "" {
			continue
		}
		f, err := parseField(r)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[f.Name]; dup {
			return nil, fmt.Errorf("model: schema names field %q twice", f.Name)
		}
		seen[f.Name] = struct{}{}
		s.Fields = append(s.Fields, f)
	}

	if len(s.Fields) == 0 {
		return nil, errors.New("model: schema has no fields")
	}
	return &s, nil
}

// parseField reads one name:type:"description" field.
func parseField(spec string) (Field, error) {
	fail := func(format string, args ...any) (Field, error) {
		return Field{}, fmt.Errorf("model: bad schema field %q: %s",
			strings.TrimSpace(spec), fmt.Sprintf(format, args...))
	}

	parts, err := splitTop(spec, ':')
	if err != nil {
		return fail("%s", err)
	}
	if len(parts) < 2 || len(parts) > 3 {
		return fail(`want name:type or name:type:"description"`)
	}

	name := strings.TrimSpace(parts[0])
	if !isIdentifier(name) {
		return fail("%q is not a usable field name", name)
	}

	typeName := strings.ToLower(strings.TrimSpace(parts[1]))
	fieldType, ok := fieldTypes[typeName]
	if !ok {
		return fail("unknown type %q, want one of %s", typeName, strings.Join(typeNames(), ", "))
	}

	f := Field{Name: name, Type: fieldType}
	if len(parts) == 3 {
		quoted := strings.TrimSpace(parts[2])
		if quoted != "" {
			description, err := strconv.Unquote(quoted)
			if err != nil {
				return fail("description %s must be in double quotes", quoted)
			}
			f.Description = description
		}
	}
	return f, nil
}

// typeNames returns every accepted type spelling, sorted, for error messages.
func typeNames() []string {
	names := make([]string, 0, len(fieldTypes))
	for n := range fieldTypes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// isIdentifier reports whether name can be used as a field name: a letter or
// underscore followed by letters, digits, or underscores. The restriction
// keeps a name from colliding with the punctuation the spec is built from.
func isIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// unwrapList strips the brackets that make a schema a list of objects rather
// than a single object, reporting which of the two was asked for. Brackets
// inside a quoted description belong to the description and are left alone.
//
// One pair around the whole spec is the only arrangement there is. A bracket
// anywhere else is asking for nesting the DSL does not have — a list inside a
// list, or a field holding one — and refusing it here says so plainly, where
// parsing on would fail later complaining about a type named "str[]".
func unwrapList(spec string) (body string, list bool, err error) {
	trimmed := strings.TrimSpace(spec)

	// Where a bracket sits is what decides the shape, so they are collected by
	// position, and only the ones outside a quoted section count.
	var found []int
	inQuote, escaped := false, false
	for i := 0; i < len(trimmed); i++ {
		switch c := trimmed[i]; {
		case escaped:
			escaped = false
		case inQuote && c == '\\':
			escaped = true
		case c == '"':
			inQuote = !inQuote
		case !inQuote && (c == '[' || c == ']'):
			found = append(found, i)
		}
	}

	switch {
	case len(found) == 0:
		return trimmed, false, nil
	case len(found) == 2 && found[0] == 0 && found[1] == len(trimmed)-1 &&
		trimmed[0] == '[' && trimmed[len(trimmed)-1] == ']':
		return trimmed[1 : len(trimmed)-1], true, nil
	}
	return "", false, errors.New("a list is written as one [ ] pair around the whole schema, " +
		"as in [id:int, name:str]; a list cannot be nested and a field cannot hold one")
}

// splitTop splits s on sep, ignoring any separator inside a double-quoted
// section so that a description may contain commas and colons. Inside quotes a
// backslash escapes the next character, and the escape is kept for Unquote.
func splitTop(s string, sep rune) ([]string, error) {
	var (
		parts   []string
		cur     strings.Builder
		inQuote bool
		escaped bool
	)
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case inQuote && r == '\\':
			cur.WriteRune(r)
			escaped = true
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == sep && !inQuote:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if inQuote {
		return nil, errors.New("unterminated quote")
	}
	return append(parts, cur.String()), nil
}

// String renders the schema back in the spelling ParseSchema accepts.
func (s *Schema) String() string {
	fields := make([]string, 0, len(s.Fields))
	for _, f := range s.Fields {
		field := f.Name + ":" + string(f.Type)
		if f.Description != "" {
			field += ":" + strconv.Quote(f.Description)
		}
		fields = append(fields, field)
	}
	joined := strings.Join(fields, ", ")
	if s.List {
		return "[" + joined + "]"
	}
	return joined
}

// Describe renders the fields as a list a model can be shown, one per line
// with its type and what it is for. It describes one object whether or not the
// answer is a list of them — saying so is the surrounding prompt's job, since
// the fields are the same either way. It is the half of a schema that goes
// into the prompt; ValidateJSON is the half that checks the result.
func (s *Schema) Describe() string {
	var b strings.Builder
	for i, f := range s.Fields {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "- %s (%s)", f.Name, f.Type)
		if f.Description != "" {
			fmt.Fprintf(&b, ": %s", f.Description)
		}
	}
	return b.String()
}

// ValidationError reports every way a document failed to match a schema, in
// one error, so that a model correcting its answer is told all of it at once
// rather than one problem per attempt.
type ValidationError struct {
	Problems []string
}

// Error implements error.
func (e *ValidationError) Error() string {
	return "model: JSON does not match the schema: " + strings.Join(e.Problems, "; ")
}

// maxBadItems bounds how many items of a list are reported on before the rest
// are summed up in a line. Every problem with a document is reported at once
// so one correction can fix all of them, but a hundred-item list that is wrong
// throughout would bury the correction it is meant to be in an instruction
// longer than the answer, and the first few items say what the mistake is.
const maxBadItems = 5

// ValidateJSON reports whether raw matches the schema exactly: every field
// present, none beyond them, and each of the declared type. A list schema
// wants a JSON array of such objects, of any length, the empty array included —
// an answer that found nothing is still an answer. Any other schema wants one
// object and nothing around it. A document that is not valid JSON at all fails
// before any of that is considered.
//
// It is deliberately strict about numbers. An int field must hold a whole
// number written without a fractional part, because 3.0 is how a decimal is
// written; a decimal field accepts either spelling.
func (s *Schema) ValidateJSON(raw string) error {
	dec := json.NewDecoder(strings.NewReader(raw))
	// Numbers stay as written, so an int can be told from a decimal.
	dec.UseNumber()

	var document any
	if err := dec.Decode(&document); err != nil {
		return fmt.Errorf("model: invalid JSON: %w", err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("model: invalid JSON: more than one value")
	}

	var problems []string
	if s.List {
		items, ok := document.([]any)
		if !ok {
			return fmt.Errorf("model: want a JSON array, got %s", jsonKind(document))
		}
		problems = s.checkItems(items)
	} else {
		object, ok := document.(map[string]any)
		if !ok {
			return fmt.Errorf("model: want a JSON object, got %s", jsonKind(document))
		}
		problems = s.checkObject(object)
	}

	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

// checkItems reports what is wrong with the items of a list answer, each
// problem named by the position it was found at, since that is the only way to
// tell one item from another. Reporting stops after maxBadItems items have
// gone wrong; what is left is counted rather than described.
func (s *Schema) checkItems(items []any) []string {
	var (
		problems []string
		bad      int
	)
	for i, item := range items {
		var found []string
		if object, ok := item.(map[string]any); ok {
			found = s.checkObject(object)
		} else {
			found = []string{fmt.Sprintf("want an object, got %s", jsonKind(item))}
		}
		if len(found) == 0 {
			continue
		}
		bad++
		if bad > maxBadItems {
			continue
		}
		for _, p := range found {
			problems = append(problems, fmt.Sprintf("item %d: %s", i, p))
		}
	}

	if bad > maxBadItems {
		problems = append(problems,
			fmt.Sprintf("%d further item(s) do not match either", bad-maxBadItems))
	}
	return problems
}

// checkObject reports every way one object fails to match the fields: a field
// missing, a field of the wrong type, or a field the schema never declared.
func (s *Schema) checkObject(object map[string]any) []string {
	var problems []string
	for _, f := range s.Fields {
		value, present := object[f.Name]
		if !present {
			problems = append(problems, fmt.Sprintf("missing field %q", f.Name))
			continue
		}
		if err := f.check(value); err != nil {
			problems = append(problems, err.Error())
		}
	}

	// Map order is unspecified, so unexpected fields are reported sorted to
	// keep the message the same from run to run.
	var unexpected []string
	for name := range object {
		if !s.has(name) {
			unexpected = append(unexpected, name)
		}
	}
	sort.Strings(unexpected)
	for _, name := range unexpected {
		problems = append(problems, fmt.Sprintf("unexpected field %q", name))
	}
	return problems
}

// has reports whether the schema declares a field by this name.
func (s *Schema) has(name string) bool {
	for _, f := range s.Fields {
		if f.Name == name {
			return true
		}
	}
	return false
}

// check reports whether value has the field's type.
func (f Field) check(value any) error {
	switch f.Type {
	case TypeString:
		if _, ok := value.(string); ok {
			return nil
		}
	case TypeBool:
		if _, ok := value.(bool); ok {
			return nil
		}
	case TypeInt:
		if n, ok := value.(json.Number); ok {
			if _, err := strconv.ParseInt(n.String(), 10, 64); err == nil {
				return nil
			}
		}
	case TypeDecimal:
		if n, ok := value.(json.Number); ok {
			if _, err := strconv.ParseFloat(n.String(), 64); err == nil {
				return nil
			}
		}
	}
	return fmt.Errorf("field %q: want %s, got %s", f.Name, f.Type, jsonKind(value))
}

// jsonKind names what a decoded JSON value turned out to be, for an error
// message. A number is quoted as written, since "want int, got a number" would
// not say what was wrong with it.
func jsonKind(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case string:
		return "a string"
	case json.Number:
		return "the number " + v.String()
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	default:
		return fmt.Sprintf("%T", value)
	}
}
