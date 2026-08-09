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
