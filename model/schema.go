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
// flat set of named scalar fields — no nesting, no arrays, no optional fields —
// which is the most a model can be relied on to produce from a prompt alone.
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

// Schema is an ordered set of fields with distinct names.
type Schema struct {
	Fields []Field
}

// ParseSchema reads the command-line spelling of a schema: comma-separated
// fields, each written name:type:"description", as in
//
//	id:int:"User ID", name:str:"Full name", active:bool:"Is active"
//
// Commas and colons inside the quoted description are part of it. The
// description may be left off entirely. An empty spec is an error, since a
// schema with no fields constrains nothing.
func ParseSchema(spec string) (*Schema, error) {
	raw, err := splitTop(spec, ',')
	if err != nil {
		return nil, fmt.Errorf("model: bad schema %q: %w", spec, err)
	}

	var s Schema
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
	return strings.Join(fields, ", ")
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

// ValidateJSON reports whether raw is a JSON object matching the schema
// exactly: every field present, none beyond them, and each of the declared
// type. A document that is not valid JSON at all fails before any of that is
// considered.
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

	object, ok := document.(map[string]any)
	if !ok {
		return fmt.Errorf("model: want a JSON object, got %s", jsonKind(document))
	}

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

	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
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
