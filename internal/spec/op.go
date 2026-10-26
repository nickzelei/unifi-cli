// Package spec holds the embedded UniFi Network OpenAPI spec and the
// operation table generated from it (ops_gen.go).
package spec

//go:generate go run ../cmd/gen ops

import "strings"

// Param is a path or query parameter.
type Param struct {
	Name        string // name in the spec, e.g. "deviceId"
	Flag        string // CLI name, e.g. "device-id"
	In          string // "path" or "query"
	Required    bool
	Type        string // "string", "integer", "boolean", ...
	Format      string
	Default     string
	Description string
}

// Op is one API operation exposed as a CLI command.
type Op struct {
	ID           string   // operationId
	Command      []string // e.g. {"dns", "policies", "list"}
	Method       string
	Path         string // e.g. "/v1/sites/{siteId}/dns/policies"
	Summary      string
	Description  string
	Tag          string
	SiteScoped   bool    // path contains {siteId}, filled from --site
	Args         []Param // path params other than siteId, in path order
	Flags        []Param // query params
	Body         bool
	BodyRequired bool
	Paged        bool   // supports offset/limit pagination
	Destructive  bool   // requires --yes
	Response     string // response schema name, if any
}

// Name is the space-joined command, e.g. "dns policies list".
func (o *Op) Name() string { return strings.Join(o.Command, " ") }

// Group is the command without its verb, e.g. "dns policies".
func (o *Op) Group() string { return strings.Join(o.Command[:len(o.Command)-1], " ") }

// Verb is the last command word, e.g. "list".
func (o *Op) Verb() string { return o.Command[len(o.Command)-1] }

// Find returns the op whose command is the longest prefix of words, and how
// many words it consumed.
func Find(words []string) (*Op, int) {
	var best *Op
	for i := range Ops {
		op := &Ops[i]
		if len(op.Command) > len(words) || (best != nil && len(op.Command) <= len(best.Command)) {
			continue
		}
		match := true
		for j, w := range op.Command {
			if words[j] != w {
				match = false
				break
			}
		}
		if match {
			best = op
		}
	}
	if best == nil {
		return nil, 0
	}
	return best, len(best.Command)
}

// ByID returns the op with the given operationId.
func ByID(id string) *Op {
	for i := range Ops {
		if Ops[i].ID == id {
			return &Ops[i]
		}
	}
	return nil
}
