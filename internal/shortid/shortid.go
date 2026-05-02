// Package shortid provides short ID generation and prefix resolution.
//
// Vendored verbatim from
// github.com/hayeah/dotfiles/libs/hayeah-go/shortid (no external
// dependency on dotfiles).
package shortid

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
)

const (
	// IDAlphabet is the character set used for ID generation (0-9a-z minus l and o).
	IDAlphabet  = "0123456789abcdefghijkmnpqrstuvwxyz"
	minQueryLen = 3
	minIDLen    = 3
	maxIDLen    = 8
	maxRetries  = 10
)

// IDTooShortError indicates the query is shorter than the minimum length.
type IDTooShortError struct {
	Query string
}

func (e *IDTooShortError) Error() string {
	return fmt.Sprintf("query %q too short (minimum %d characters)", e.Query, minQueryLen)
}

// AmbiguousIDError indicates multiple candidates match the query prefix.
type AmbiguousIDError struct {
	Query   string
	Matches []string
}

func (e *AmbiguousIDError) Error() string {
	return fmt.Sprintf("ambiguous prefix %q: matches %v", e.Query, e.Matches)
}

// IDNotFoundError indicates no candidate matches the query.
type IDNotFoundError struct {
	Query string
}

func (e *IDNotFoundError) Error() string {
	return fmt.Sprintf("no match for %q", e.Query)
}

// Generate creates a unique short ID not in existing.
// Starts at 3 chars, grows up to 8 on collision (10 retries per length).
// Uses crypto/rand for cryptographic randomness.
func Generate(existing map[string]bool) (string, error) {
	alphabetLen := len(IDAlphabet)
	buf := make([]byte, maxIDLen)

	for length := minIDLen; length <= maxIDLen; length++ {
		for i := 0; i < maxRetries; i++ {
			if _, err := rand.Read(buf[:length]); err != nil {
				return "", err
			}
			id := make([]byte, length)
			for j := 0; j < length; j++ {
				id[j] = IDAlphabet[buf[j]%byte(alphabetLen)]
			}
			s := string(id)
			if !existing[s] {
				return s, nil
			}
		}
	}
	return "", errors.New("could not generate a unique id after exhausting retries")
}

// Resolve resolves a prefix query against candidates. Case-insensitive.
// Returns the single matching candidate. Returns an error on ambiguity,
// no match, or too-short query.
func Resolve(query string, candidates []string) (string, error) {
	if len(query) < minQueryLen {
		return "", &IDTooShortError{Query: query}
	}

	q := strings.ToLower(query)
	var matches []string

	for _, c := range candidates {
		if strings.ToLower(c) == q {
			return c, nil // exact match
		}
		if strings.HasPrefix(strings.ToLower(c), q) {
			matches = append(matches, c)
		}
	}

	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", &AmbiguousIDError{Query: query, Matches: matches}
	}
	return "", &IDNotFoundError{Query: query}
}
