package model

import (
	"errors"
	"strings"
	"testing"
)

// The spelling from the command line is the one that has to work.
func TestParseSchema(t *testing.T) {
	s, err := ParseSchema(`id:int:"User ID", name:str:"Full name", active:bool:"Is active", rate:dec:"Hourly rate"`)
	if err != nil {
		t.Fatalf("ParseSchema() = %v, want nil", err)
	}

	want := []Field{
		{Name: "id", Type: TypeInt, Description: "User ID"},
		{Name: "name", Type: TypeString, Description: "Full name"},
		{Name: "active", Type: TypeBool, Description: "Is active"},
		{Name: "rate", Type: TypeDecimal, Description: "Hourly rate"},
	}
	if len(s.Fields) != len(want) {
		t.Fatalf("parsed %d fields, want %d", len(s.Fields), len(want))
	}
	for i, f := range s.Fields {
		if f != want[i] {
			t.Errorf("field %d = %+v, want %+v", i, f, want[i])
		}
	}
}

func TestParseSchemaAcceptedSpellings(t *testing.T) {
	tests := []struct {
		name string
		spec string
		want []Field
	}{
		{
			"long type names",
			"a:string, b:boolean, c:integer, d:decimal",
			[]Field{
				{Name: "a", Type: TypeString},
				{Name: "b", Type: TypeBool},
				{Name: "c", Type: TypeInt},
				{Name: "d", Type: TypeDecimal},
			},
		},
		{
			"uppercase type",
			"a:INT",
			[]Field{{Name: "a", Type: TypeInt}},
		},
		{
			"surrounding whitespace",
			`   id : int : "User ID"  ,  name:str   `,
			[]Field{
				{Name: "id", Type: TypeInt, Description: "User ID"},
				{Name: "name", Type: TypeString},
			},
		},
		{
			"punctuation inside the description",
			`name:str:"Last, first: as printed", id:int`,
			[]Field{
				{Name: "name", Type: TypeString, Description: "Last, first: as printed"},
				{Name: "id", Type: TypeInt},
			},
		},
		{
			"escaped quote inside the description",
			`name:str:"The \"display\" name"`,
			[]Field{{Name: "name", Type: TypeString, Description: `The "display" name`}},
		},
		{
			"empty description",
			`name:str:`,
			[]Field{{Name: "name", Type: TypeString}},
		},
		{
			"doubled and trailing commas",
			"a:int,,b:str,",
			[]Field{{Name: "a", Type: TypeInt}, {Name: "b", Type: TypeString}},
		},
		{
			"underscores and digits in names",
			"user_id:int, line2:str",
			[]Field{{Name: "user_id", Type: TypeInt}, {Name: "line2", Type: TypeString}},
		},
		{
			"a range with both ends",
			"age:int(0..120)",
			[]Field{{Name: "age", Type: TypeInt, Range: Range{Min: 0, Max: 120, HasMin: true, HasMax: true}}},
		},
		{
			"a minimum with no maximum",
			"votes:int(0..)",
			[]Field{{Name: "votes", Type: TypeInt, Range: Range{Min: 0, HasMin: true}}},
		},
		{
			"a maximum with no minimum",
			"discount:dec(..0.5)",
			[]Field{{Name: "discount", Type: TypeDecimal, Range: Range{Max: 0.5, HasMax: true}}},
		},
		{
			"negative bounds",
			"temp:dec(-40..60)",
			[]Field{{Name: "temp", Type: TypeDecimal, Range: Range{Min: -40, Max: 60, HasMin: true, HasMax: true}}},
		},
		{
			"a bound written with an exponent",
			"budget:dec(..1e3)",
			[]Field{{Name: "budget", Type: TypeDecimal, Range: Range{Max: 1000, HasMax: true}}},
		},
		{
			"whitespace inside and around the range",
			"age:int ( 0 .. 120 )",
			[]Field{{Name: "age", Type: TypeInt, Range: Range{Min: 0, Max: 120, HasMin: true, HasMax: true}}},
		},
		{
			"a range on a long type name",
			"age:integer(0..120)",
			[]Field{{Name: "age", Type: TypeInt, Range: Range{Min: 0, Max: 120, HasMin: true, HasMax: true}}},
		},
		{
			"a range and a description together",
			`age:int(0..120):"Age in years"`,
			[]Field{{
				Name:        "age",
				Type:        TypeInt,
				Description: "Age in years",
				Range:       Range{Min: 0, Max: 120, HasMin: true, HasMax: true},
			}},
		},
		{
			"a range inside a list",
			"[rank:int(1..10), name:str]",
			[]Field{
				{Name: "rank", Type: TypeInt, Range: Range{Min: 1, Max: 10, HasMin: true, HasMax: true}},
				{Name: "name", Type: TypeString},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := ParseSchema(tt.spec)
			if err != nil {
				t.Fatalf("ParseSchema(%q) = %v, want nil", tt.spec, err)
			}
			if len(s.Fields) != len(tt.want) {
				t.Fatalf("parsed %d fields, want %d", len(s.Fields), len(tt.want))
			}
			for i, f := range s.Fields {
				if f != tt.want[i] {
					t.Errorf("field %d = %+v, want %+v", i, f, tt.want[i])
				}
			}
		})
	}
}

// Brackets around the whole spec ask for a list of objects. The fields are
// read exactly as they are without them, so the only difference is the flag.
func TestParseSchemaList(t *testing.T) {
	tests := []struct {
		name string
		spec string
		want []Field
	}{
		{
			"the shape in the flag's usage",
			"[id:int, name:str]",
			[]Field{{Name: "id", Type: TypeInt}, {Name: "name", Type: TypeString}},
		},
		{
			"descriptions inside a list",
			`[id:int:"User ID", name:str:"Full name"]`,
			[]Field{
				{Name: "id", Type: TypeInt, Description: "User ID"},
				{Name: "name", Type: TypeString, Description: "Full name"},
			},
		},
		{
			"whitespace around and inside the brackets",
			"  [ id:int , name:str ]  ",
			[]Field{{Name: "id", Type: TypeInt}, {Name: "name", Type: TypeString}},
		},
		{
			"one field",
			"[name:str]",
			[]Field{{Name: "name", Type: TypeString}},
		},
		{
			// A bracket inside a description is part of the description, so
			// the one that closes the list is still the last character.
			"brackets inside a description",
			`[name:str:"Surname [as printed]"]`,
			[]Field{{Name: "name", Type: TypeString, Description: "Surname [as printed]"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := ParseSchema(tt.spec)
			if err != nil {
				t.Fatalf("ParseSchema(%q) = %v, want nil", tt.spec, err)
			}
			if !s.List {
				t.Errorf("ParseSchema(%q).List = false, want true", tt.spec)
			}
			if len(s.Fields) != len(tt.want) {
				t.Fatalf("parsed %d fields, want %d", len(s.Fields), len(tt.want))
			}
			for i, f := range s.Fields {
				if f != tt.want[i] {
					t.Errorf("field %d = %+v, want %+v", i, f, tt.want[i])
				}
			}
		})
	}
}

// Without brackets the answer is one object, which is what it has always been.
func TestParseSchemaWithoutBracketsIsNotAList(t *testing.T) {
	s, err := ParseSchema(`id:int:"User ID", name:str`)
	if err != nil {
		t.Fatalf("ParseSchema() = %v, want nil", err)
	}
	if s.List {
		t.Error("List = true, want false for a spec with no brackets")
	}
	// A bracket inside a description does not make one either.
	s, err = ParseSchema(`name:str:"Surname [as printed]"`)
	if err != nil {
		t.Fatalf("ParseSchema() = %v, want nil", err)
	}
	if s.List {
		t.Error("List = true, want false for a bracket inside a description")
	}
}

func TestParseSchemaErrors(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		wantErr string
	}{
		{"empty", "", "has no fields"},
		{"only separators", " , , ", "has no fields"},
		{"no type", "id", "want name:type"},
		{"too many parts", `id:int:"User ID":extra`, "want name:type"},
		{"unknown type", "id:number", `unknown type "number"`},
		{"missing type", "id:", `unknown type ""`},
		{"no name", ":int", "is not a usable field name"},
		{"name starting with a digit", "2fast:int", "is not a usable field name"},
		{"name with punctuation", "user-id:int", "is not a usable field name"},
		{"duplicate names", "id:int, id:str", `names field "id" twice`},
		{"unterminated quote", `id:int:"User ID`, "unterminated quote"},
		{"unquoted description", "id:int:User ID", "must be in double quotes"},
		{"an empty list", "[]", "has no fields"},
		{"an empty list of separators", "[ , ]", "has no fields"},
		{"an unclosed list", "[id:int, name:str", "one [ ] pair"},
		{"an unopened list", "id:int, name:str]", "one [ ] pair"},
		{"closed before it opens", "]id:int[", "one [ ] pair"},
		{"a list nested in a list", "[[id:int], [name:str]]", "cannot be nested"},
		{"a field holding a list", "[id:int, names:str[]]", "cannot be nested"},
		{"a field holding a list, no outer list", "id:int, names:str[]", "one [ ] pair"},
		{"brackets around one field", "id:int, [name:str]", "one [ ] pair"},
		{"a range on a string", "name:str(1..5)", "only for int and decimal"},
		{"a range on a bool", "ok:bool(0..1)", "only for int and decimal"},
		{"a fractional bound on an int", "age:int(1.5..3)", "ends at a whole number"},
		{"a minimum above the maximum", "age:int(120..0)", "is above the maximum"},
		{"a range with neither end", "age:int(..)", "needs a minimum, a maximum, or both"},
		{"an empty range", "age:int()", "written low..high"},
		{"a range with no dots", "age:int(0)", "written low..high"},
		{"an unclosed range", "age:int(0..120", "missing its closing parenthesis"},
		{"a range with no opening parenthesis", "age:int0..120)", "goes in parentheses"},
		{"text after the range", "age:int(0..120)x", `"x" cannot follow`},
		{"a nested range", "age:int((0..120)", "one ( ) pair"},
		{"a bound that is not a number", "age:int(a..b)", `"a" is not a number`},
		{"a bound that is not a number at all", "age:dec(nan..1)", `"nan" is not a number`},
		{"an infinite bound", "age:dec(..inf)", `"inf" is not a number`},
		{"a range on an unknown type", "age:number(0..9)", `unknown type "number"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := ParseSchema(tt.spec)
			if err == nil {
				t.Fatalf("ParseSchema(%q) = %v, want an error", tt.spec, s)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ParseSchema(%q) = %q, want it to mention %q", tt.spec, err, tt.wantErr)
			}
		})
	}
}

// String renders the spelling ParseSchema reads, so a round trip is stable.
func TestSchemaString(t *testing.T) {
	const spec = `id:int:"User ID", name:string, rate:decimal:"Hourly rate"`
	s, err := ParseSchema(spec)
	if err != nil {
		t.Fatalf("ParseSchema() = %v, want nil", err)
	}

	const want = `id:int:"User ID", name:string, rate:decimal:"Hourly rate"`
	if got := s.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}

	again, err := ParseSchema(s.String())
	if err != nil {
		t.Fatalf("reparsing %q = %v, want nil", s.String(), err)
	}
	if again.String() != want {
		t.Errorf("round trip = %q, want %q", again.String(), want)
	}
}

// A list renders with the brackets that made it one, so it round trips too.
func TestSchemaStringList(t *testing.T) {
	s, err := ParseSchema(`[id:int:"User ID", name:str]`)
	if err != nil {
		t.Fatalf("ParseSchema() = %v, want nil", err)
	}

	const want = `[id:int:"User ID", name:string]`
	if got := s.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}

	again, err := ParseSchema(s.String())
	if err != nil {
		t.Fatalf("reparsing %q = %v, want nil", s.String(), err)
	}
	if !again.List {
		t.Error("round trip lost List")
	}
	if again.String() != want {
		t.Errorf("round trip = %q, want %q", again.String(), want)
	}
}

// A range is part of the spelling, so it has to survive the round trip too.
// The bounds come back canonicalised the way the type names do: 1e3 is
// rendered 1000, which reads back as the same number.
func TestSchemaStringRange(t *testing.T) {
	const spec = `age:int(0..120):"Age in years", votes:int(0..), discount:dec(..1e3), temp:dec(-40..60)`
	s, err := ParseSchema(spec)
	if err != nil {
		t.Fatalf("ParseSchema() = %v, want nil", err)
	}

	const want = `age:int(0..120):"Age in years", votes:int(0..), discount:decimal(..1000), temp:decimal(-40..60)`
	if got := s.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}

	again, err := ParseSchema(s.String())
	if err != nil {
		t.Fatalf("reparsing %q = %v, want nil", s.String(), err)
	}
	if again.String() != want {
		t.Errorf("round trip = %q, want %q", again.String(), want)
	}
}

// testSchema is the schema the validation tests are written against.
func testSchema(t *testing.T) *Schema {
	t.Helper()
	s, err := ParseSchema(`id:int:"User ID", name:str:"Full name", active:bool:"Is active", rate:dec:"Hourly rate"`)
	if err != nil {
		t.Fatalf("ParseSchema() = %v, want nil", err)
	}
	return s
}

func TestValidateJSONAccepts(t *testing.T) {
	tests := []struct {
		name string
		doc  string
	}{
		{
			"every field",
			`{"id": 1, "name": "Ada", "active": true, "rate": 12.5}`,
		},
		{
			"fields in another order",
			`{"rate": 12.5, "active": false, "name": "Ada", "id": 1}`,
		},
		{
			"a whole number for a decimal",
			`{"id": 1, "name": "Ada", "active": true, "rate": 12}`,
		},
		{
			"negative numbers",
			`{"id": -1, "name": "", "active": false, "rate": -0.5}`,
		},
		{
			"an exponent for a decimal",
			`{"id": 1, "name": "Ada", "active": true, "rate": 1.25e2}`,
		},
		{
			"surrounding whitespace",
			"\n\t" + `{"id": 1, "name": "Ada", "active": true, "rate": 12.5}` + "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := testSchema(t).ValidateJSON(tt.doc); err != nil {
				t.Errorf("ValidateJSON(%s) = %v, want nil", tt.doc, err)
			}
		})
	}
}

func TestValidateJSONRejects(t *testing.T) {
	tests := []struct {
		name     string
		doc      string
		problems []string
	}{
		{
			"a missing field",
			`{"id": 1, "name": "Ada", "active": true}`,
			[]string{`missing field "rate"`},
		},
		{
			"a field beyond the schema",
			`{"id": 1, "name": "Ada", "active": true, "rate": 12.5, "email": "a@b.c"}`,
			[]string{`unexpected field "email"`},
		},
		{
			"a number where a string belongs",
			`{"id": 1, "name": 7, "active": true, "rate": 12.5}`,
			[]string{`field "name": want string, got the number 7`},
		},
		{
			"a string where a number belongs",
			`{"id": "1", "name": "Ada", "active": true, "rate": 12.5}`,
			[]string{`field "id": want int, got a string`},
		},
		{
			"a fractional int",
			`{"id": 1.5, "name": "Ada", "active": true, "rate": 12.5}`,
			[]string{`field "id": want int, got the number 1.5`},
		},
		{
			"a whole number written as a decimal for an int",
			`{"id": 1.0, "name": "Ada", "active": true, "rate": 12.5}`,
			[]string{`field "id": want int, got the number 1.0`},
		},
		{
			"a string where a boolean belongs",
			`{"id": 1, "name": "Ada", "active": "yes", "rate": 12.5}`,
			[]string{`field "active": want bool, got a string`},
		},
		{
			"null",
			`{"id": null, "name": "Ada", "active": true, "rate": 12.5}`,
			[]string{`field "id": want int, got null`},
		},
		{
			"a nested object",
			`{"id": 1, "name": {"first": "Ada"}, "active": true, "rate": 12.5}`,
			[]string{`field "name": want string, got an object`},
		},
		{
			"an array",
			`{"id": 1, "name": ["Ada"], "active": true, "rate": 12.5}`,
			[]string{`field "name": want string, got an array`},
		},
		{
			"everything at once, in schema order then sorted",
			`{"name": 7, "zeta": 1, "alpha": 2}`,
			[]string{
				`missing field "id"`,
				`field "name": want string, got the number 7`,
				`missing field "active"`,
				`missing field "rate"`,
				`unexpected field "alpha"`,
				`unexpected field "zeta"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := testSchema(t).ValidateJSON(tt.doc)
			if err == nil {
				t.Fatalf("ValidateJSON(%s) = nil, want an error", tt.doc)
			}

			var invalid *ValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("ValidateJSON(%s) = %v (%T), want a *ValidationError", tt.doc, err, err)
			}
			if len(invalid.Problems) != len(tt.problems) {
				t.Fatalf("problems = %q, want %q", invalid.Problems, tt.problems)
			}
			for i, p := range invalid.Problems {
				if p != tt.problems[i] {
					t.Errorf("problem %d = %q, want %q", i, p, tt.problems[i])
				}
			}
		})
	}
}

// rangeSchema is the schema the range tests are written against: one field of
// each shape of range, and one number with none at all.
func rangeSchema(t *testing.T) *Schema {
	t.Helper()
	s, err := ParseSchema(`age:int(0..120), score:dec(0..1), votes:int(0..), discount:dec(..0.5), free:int`)
	if err != nil {
		t.Fatalf("ParseSchema() = %v, want nil", err)
	}
	return s
}

// Both ends of a range are included, so a value sitting exactly on a bound is
// an answer and not a near miss.
func TestValidateJSONRangeAccepts(t *testing.T) {
	tests := []struct {
		name string
		doc  string
	}{
		{
			"values well inside every range",
			`{"age": 34, "score": 0.5, "votes": 7, "discount": 0.25, "free": 1}`,
		},
		{
			"values on the lower bounds",
			`{"age": 0, "score": 0, "votes": 0, "discount": -1000, "free": -1000}`,
		},
		{
			"values on the upper bounds",
			`{"age": 120, "score": 1, "votes": 1000000, "discount": 0.5, "free": 1000000}`,
		},
		{
			"a whole number for a bounded decimal",
			`{"age": 34, "score": 1, "votes": 7, "discount": 0, "free": 1}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := rangeSchema(t).ValidateJSON(tt.doc); err != nil {
				t.Errorf("ValidateJSON(%s) = %v, want nil", tt.doc, err)
			}
		})
	}
}

// A number outside its range is reported like any other field problem, in the
// same voice and in the same list, so that one correction fixes all of it.
func TestValidateJSONRangeRejects(t *testing.T) {
	tests := []struct {
		name     string
		doc      string
		problems []string
	}{
		{
			"below the minimum",
			`{"age": -1, "score": 0.5, "votes": 7, "discount": 0.25, "free": 1}`,
			[]string{`field "age": want int between 0 and 120, got the number -1`},
		},
		{
			"above the maximum",
			`{"age": 150, "score": 0.5, "votes": 7, "discount": 0.25, "free": 1}`,
			[]string{`field "age": want int between 0 and 120, got the number 150`},
		},
		{
			"below a minimum with no maximum",
			`{"age": 34, "score": 0.5, "votes": -3, "discount": 0.25, "free": 1}`,
			[]string{`field "votes": want int 0 or more, got the number -3`},
		},
		{
			"above a maximum with no minimum",
			`{"age": 34, "score": 0.5, "votes": 7, "discount": 0.9, "free": 1}`,
			[]string{`field "discount": want decimal 0.5 or less, got the number 0.9`},
		},
		{
			"just outside a decimal range",
			`{"age": 34, "score": 1.0001, "votes": 7, "discount": 0.25, "free": 1}`,
			[]string{`field "score": want decimal between 0 and 1, got the number 1.0001`},
		},
		{
			"the wrong type beats the range to it",
			`{"age": "34", "score": 0.5, "votes": 7, "discount": 0.25, "free": 1}`,
			[]string{`field "age": want int, got a string`},
		},
		{
			"a range problem beside an unrelated one",
			`{"age": 150, "score": 0.5, "votes": 7, "discount": 0.25}`,
			[]string{
				`field "age": want int between 0 and 120, got the number 150`,
				`missing field "free"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := rangeSchema(t).ValidateJSON(tt.doc)
			if err == nil {
				t.Fatalf("ValidateJSON(%s) = nil, want an error", tt.doc)
			}

			var invalid *ValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("ValidateJSON(%s) = %v (%T), want a *ValidationError", tt.doc, err, err)
			}
			if len(invalid.Problems) != len(tt.problems) {
				t.Fatalf("problems = %q, want %q", invalid.Problems, tt.problems)
			}
			for i, p := range invalid.Problems {
				if p != tt.problems[i] {
					t.Errorf("problem %d = %q, want %q", i, p, tt.problems[i])
				}
			}
		})
	}
}

// A range inside a list is checked item by item, and the problem names the
// item it was found in like every other problem with a list does.
func TestValidateJSONRangeInAList(t *testing.T) {
	s, err := ParseSchema(`[rank:int(1..10), name:str]`)
	if err != nil {
		t.Fatalf("ParseSchema() = %v, want nil", err)
	}

	const doc = `[{"rank": 1, "name": "Ada"}, {"rank": 10, "name": "Grace"}, {"rank": 44, "name": "Alan"}]`
	err = s.ValidateJSON(doc)
	if err == nil {
		t.Fatalf("ValidateJSON(%s) = nil, want an error", doc)
	}

	const want = `item 2: field "rank": want int between 1 and 10, got the number 44`
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("ValidateJSON(%s) = %v (%T), want a *ValidationError", doc, err, err)
	}
	if len(invalid.Problems) != 1 || invalid.Problems[0] != want {
		t.Errorf("problems = %q, want %q", invalid.Problems, []string{want})
	}
}

// Malformed input fails before the schema is considered at all, so these are
// plain errors rather than a list of unmet field requirements.
func TestValidateJSONMalformed(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{"empty", "", "invalid JSON"},
		{"truncated", `{"id": 1`, "invalid JSON"},
		{"prose", "Sure! Here is the JSON:", "invalid JSON"},
		{"two documents", `{"id": 1} {"id": 2}`, "more than one value"},
		{"an array at the top level", `[{"id": 1}]`, "want a JSON object, got an array"},
		{"a string at the top level", `"done"`, "want a JSON object, got a string"},
		{"null at the top level", `null`, "want a JSON object, got null"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := testSchema(t).ValidateJSON(tt.doc)
			if err == nil {
				t.Fatalf("ValidateJSON(%q) = nil, want an error", tt.doc)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ValidateJSON(%q) = %q, want it to mention %q", tt.doc, err, tt.wantErr)
			}
			var invalid *ValidationError
			if errors.As(err, &invalid) {
				t.Errorf("ValidateJSON(%q) = a *ValidationError, want a plain error", tt.doc)
			}
		})
	}
}

// A single-field schema is the smallest thing that can be asked for.
func TestValidateJSONSingleField(t *testing.T) {
	s, err := ParseSchema(`answer:str:"The answer"`)
	if err != nil {
		t.Fatalf("ParseSchema() = %v, want nil", err)
	}
	if err := s.ValidateJSON(`{"answer": "42"}`); err != nil {
		t.Errorf("ValidateJSON() = %v, want nil", err)
	}
	if err := s.ValidateJSON(`{}`); err == nil {
		t.Error("ValidateJSON({}) = nil, want an error")
	}
}

// Every problem is reported in one message, so a model correcting its answer
// is told all of them at once.
func TestValidationErrorMessage(t *testing.T) {
	err := testSchema(t).ValidateJSON(`{"id": "1", "name": "Ada", "active": true}`)
	const want = `model: JSON does not match the schema: field "id": want int, got a string; missing field "rate"`
	if err == nil || err.Error() != want {
		t.Errorf("ValidateJSON() = %v, want %q", err, want)
	}
}

// testListSchema is the same fields as testSchema, asked for as a list.
func testListSchema(t *testing.T) *Schema {
	t.Helper()
	s, err := ParseSchema(`[id:int:"User ID", name:str:"Full name", active:bool:"Is active", rate:dec:"Hourly rate"]`)
	if err != nil {
		t.Fatalf("ParseSchema() = %v, want nil", err)
	}
	return s
}

func TestValidateJSONListAccepts(t *testing.T) {
	tests := []struct {
		name string
		doc  string
	}{
		{
			"one item",
			`[{"id": 1, "name": "Ada", "active": true, "rate": 12.5}]`,
		},
		{
			"several items",
			`[{"id": 1, "name": "Ada", "active": true, "rate": 12.5},
			  {"id": 2, "name": "Grace", "active": false, "rate": 0}]`,
		},
		{
			// An answer that found nothing is still an answer, so the empty
			// array is a match rather than a failed attempt.
			"no items",
			`[]`,
		},
		{
			"surrounding whitespace",
			"\n\t" + `[{"id": 1, "name": "Ada", "active": true, "rate": 12.5}]` + "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := testListSchema(t).ValidateJSON(tt.doc); err != nil {
				t.Errorf("ValidateJSON(%s) = %v, want nil", tt.doc, err)
			}
		})
	}
}

// Every problem names the item it was found in, since position is the only
// thing that tells one item of a list from another.
func TestValidateJSONListRejects(t *testing.T) {
	tests := []struct {
		name     string
		doc      string
		problems []string
	}{
		{
			"a missing field in the only item",
			`[{"id": 1, "name": "Ada", "active": true}]`,
			[]string{`item 0: missing field "rate"`},
		},
		{
			"a bad field in the second item",
			`[{"id": 1, "name": "Ada", "active": true, "rate": 12.5},
			  {"id": "2", "name": "Grace", "active": false, "rate": 0}]`,
			[]string{`item 1: field "id": want int, got a string`},
		},
		{
			"an item that is not an object",
			`["Ada", {"id": 1, "name": "Ada", "active": true, "rate": 12.5}]`,
			[]string{`item 0: want an object, got a string`},
		},
		{
			"a list nested inside the list",
			`[[{"id": 1, "name": "Ada", "active": true, "rate": 12.5}]]`,
			[]string{`item 0: want an object, got an array`},
		},
		{
			"several problems in one item",
			`[{"name": 7, "zeta": 1}]`,
			[]string{
				`item 0: missing field "id"`,
				`item 0: field "name": want string, got the number 7`,
				`item 0: missing field "active"`,
				`item 0: missing field "rate"`,
				`item 0: unexpected field "zeta"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := testListSchema(t).ValidateJSON(tt.doc)
			if err == nil {
				t.Fatalf("ValidateJSON(%s) = nil, want an error", tt.doc)
			}

			var invalid *ValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("ValidateJSON(%s) = %v (%T), want a *ValidationError", tt.doc, err, err)
			}
			if len(invalid.Problems) != len(tt.problems) {
				t.Fatalf("problems = %q, want %q", invalid.Problems, tt.problems)
			}
			for i, p := range invalid.Problems {
				if p != tt.problems[i] {
					t.Errorf("problem %d = %q, want %q", i, p, tt.problems[i])
				}
			}
		})
	}
}

// A list that is wrong throughout is reported on up to a point and then
// counted, so that the correction handed back to the model stays readable.
func TestValidateJSONListStopsDescribingAfterAFewBadItems(t *testing.T) {
	items := make([]string, 0, maxBadItems+3)
	for range maxBadItems + 3 {
		items = append(items, `{}`)
	}
	doc := "[" + strings.Join(items, ", ") + "]"

	err := testListSchema(t).ValidateJSON(doc)
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("ValidateJSON() = %v (%T), want a *ValidationError", err, err)
	}

	// Four missing fields for each of the first maxBadItems items, plus the
	// line that counts the rest.
	want := 4*maxBadItems + 1
	if len(invalid.Problems) != want {
		t.Fatalf("reported %d problems, want %d: %q", len(invalid.Problems), want, invalid.Problems)
	}
	if last := invalid.Problems[len(invalid.Problems)-1]; last != "3 further item(s) do not match either" {
		t.Errorf("last problem = %q, want the count of the items left over", last)
	}
	for _, p := range invalid.Problems[:len(invalid.Problems)-1] {
		if strings.HasPrefix(p, "item 5") || strings.HasPrefix(p, "item 6") || strings.HasPrefix(p, "item 7") {
			t.Errorf("problem %q describes an item past the first %d", p, maxBadItems)
		}
	}
}

// A list schema wants an array and a plain schema wants an object, so the
// document each one accepts is the other's failure.
func TestValidateJSONListAndObjectAreNotInterchangeable(t *testing.T) {
	const object = `{"id": 1, "name": "Ada", "active": true, "rate": 12.5}`
	const array = "[" + object + "]"

	err := testListSchema(t).ValidateJSON(object)
	if err == nil || !strings.Contains(err.Error(), "want a JSON array, got an object") {
		t.Errorf("a list schema given an object = %v, want it to ask for an array", err)
	}
	err = testSchema(t).ValidateJSON(array)
	if err == nil || !strings.Contains(err.Error(), "want a JSON object, got an array") {
		t.Errorf("a plain schema given an array = %v, want it to ask for an object", err)
	}
}

// Describe is the half of a schema that goes into the prompt.
func TestSchemaDescribe(t *testing.T) {
	s, err := ParseSchema(`id:int:"User ID", name:str, rate:dec:"Hourly rate"`)
	if err != nil {
		t.Fatalf("ParseSchema() = %v, want nil", err)
	}
	const want = "- id (int): User ID\n- name (string)\n- rate (decimal): Hourly rate"
	if got := s.Describe(); got != want {
		t.Errorf("Describe() = %q, want %q", got, want)
	}

	// A list has the same fields, so it is described the same way: that they
	// arrive as an array is the surrounding instruction's business.
	list, err := ParseSchema(`[id:int:"User ID", name:str, rate:dec:"Hourly rate"]`)
	if err != nil {
		t.Fatalf("ParseSchema() = %v, want nil", err)
	}
	if got := list.Describe(); got != want {
		t.Errorf("Describe() of a list = %q, want %q", got, want)
	}
}

// A range reaches the model as a sentence of its own, after the description
// where there is one. The description is given the full stop it needs for the
// two to read as English, and is left alone where it already ends in one.
func TestSchemaDescribeRange(t *testing.T) {
	s, err := ParseSchema(`age:int(0..120):"Age in years", score:dec(0..1), votes:int(0..), ` +
		`discount:dec(..0.5), note:str:"Free text.", rate:dec:"Hourly rate"`)
	if err != nil {
		t.Fatalf("ParseSchema() = %v, want nil", err)
	}

	want := strings.Join([]string{
		"- age (int): Age in years. Must be between 0 and 120, inclusive.",
		"- score (decimal): Must be between 0 and 1, inclusive.",
		"- votes (int): Must be 0 or more.",
		"- discount (decimal): Must be 0.5 or less.",
		"- note (string): Free text.",
		"- rate (decimal): Hourly rate",
	}, "\n")
	if got := s.Describe(); got != want {
		t.Errorf("Describe() = %q, want %q", got, want)
	}
}
