package shortid

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

type resolveTest struct {
	Name       string   `json:"name"`
	Query      string   `json:"query"`
	Candidates []string `json:"candidates"`
	Expected   string   `json:"expected"`
}

type resolveErrorTest struct {
	Name       string   `json:"name"`
	Query      string   `json:"query"`
	Candidates []string `json:"candidates"`
	Error      string   `json:"error"`
}

type testData struct {
	Alphabet          string             `json:"alphabet"`
	AlphabetSize      int                `json:"alphabet_size"`
	MinLength         int                `json:"min_length"`
	MaxLength         int                `json:"max_length"`
	ResolveTests      []resolveTest      `json:"resolve_tests"`
	ResolveErrorTests []resolveErrorTest `json:"resolve_error_tests"`
}

func loadTestData(t *testing.T) testData {
	t.Helper()
	data, err := os.ReadFile("testdata/shortid.json")
	if err != nil {
		t.Fatal(err)
	}
	var td testData
	if err := json.Unmarshal(data, &td); err != nil {
		t.Fatal(err)
	}
	return td
}

func TestGenerate(t *testing.T) {
	td := loadTestData(t)

	t.Run("produces IDs from correct alphabet", func(t *testing.T) {
		id, err := Generate(nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(id) < td.MinLength || len(id) > td.MaxLength {
			t.Fatalf("id length %d not in [%d, %d]", len(id), td.MinLength, td.MaxLength)
		}
		for _, ch := range id {
			if !containsRune(td.Alphabet, ch) {
				t.Fatalf("char %c not in alphabet", ch)
			}
		}
	})

	t.Run("does not collide with existing", func(t *testing.T) {
		existing := map[string]bool{"a3f": true, "b7k": true}
		id, err := Generate(existing)
		if err != nil {
			t.Fatal(err)
		}
		if existing[id] {
			t.Fatalf("generated id %q collides with existing", id)
		}
	})

	t.Run("generates unique IDs over many calls", func(t *testing.T) {
		seen := make(map[string]bool)
		for i := 0; i < 100; i++ {
			id, err := Generate(seen)
			if err != nil {
				t.Fatal(err)
			}
			if seen[id] {
				t.Fatalf("duplicate id %q", id)
			}
			seen[id] = true
		}
	})
}

func TestResolve(t *testing.T) {
	td := loadTestData(t)
	for _, tc := range td.ResolveTests {
		t.Run(tc.Name, func(t *testing.T) {
			result, err := Resolve(tc.Query, tc.Candidates)
			if err != nil {
				t.Fatal(err)
			}
			if result != tc.Expected {
				t.Fatalf("got %q, want %q", result, tc.Expected)
			}
		})
	}
}

func TestResolveErrors(t *testing.T) {
	td := loadTestData(t)
	for _, tc := range td.ResolveErrorTests {
		t.Run(tc.Name, func(t *testing.T) {
			_, err := Resolve(tc.Query, tc.Candidates)
			if err == nil {
				t.Fatal("expected error")
			}
			switch tc.Error {
			case "IDTooShortError":
				var target *IDTooShortError
				if !errors.As(err, &target) {
					t.Fatalf("expected IDTooShortError, got %T: %v", err, err)
				}
			case "AmbiguousIDError":
				var target *AmbiguousIDError
				if !errors.As(err, &target) {
					t.Fatalf("expected AmbiguousIDError, got %T: %v", err, err)
				}
			case "IDNotFoundError":
				var target *IDNotFoundError
				if !errors.As(err, &target) {
					t.Fatalf("expected IDNotFoundError, got %T: %v", err, err)
				}
			}
		})
	}
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
