package nearhostrc

import "github.com/nearhost/nearhost/internal/config"

// Kind is what a project's .nearhostrc, as read, asks of it.
type Kind int

const (
	// Nothing: there is no file and none was approved, or the file is
	// the approved one.
	Nothing Kind = iota
	// Updated: the file changed only what needs no approval.
	Updated
	// Pending: the file asks for a host or main name that needs the
	// user's approval.
	Pending
	// Problem: the file cannot be read or merged, or is gone after being
	// approved.
	Problem
)

// Class is how a read .nearhostrc stands against a project: its kind, the
// project it merges to and what of that needs approval, or the problem.
type Class struct {
	Kind    Kind
	Next    config.Project
	Items   []Item
	Problem string
}

// Classify says how the .nearhostrc that Read returned as f,
// found and err stands against p and its approved snapshot. It is the
// review's judgement, kept in one place so that export says a change was
// waiting for approval exactly when the review would have held it. The
// routes the file doesn't list are kept from p: there is no discovery run at
// start to say what else the project has.
func Classify(p config.Project, f *File, found bool, err error) Class {
	switch {
	case err != nil:
		return Class{Kind: Problem, Problem: err.Error()}
	case !found && p.Nearhostrc == nil:
		return Class{Kind: Nothing}
	case !found:
		return Class{Kind: Problem, Problem: FileName + " is gone; serving what was last approved"}
	case p.Nearhostrc != nil && f.SHA256 == p.Nearhostrc.SHA256:
		return Class{Kind: Nothing}
	}
	next, err := Merge(p, f, nil)
	if err != nil {
		return Class{Kind: Problem, Problem: err.Error()}
	}
	if items := NeedsApproval(p, next); len(items) > 0 {
		return Class{Kind: Pending, Next: next, Items: items}
	}
	return Class{Kind: Updated, Next: next}
}
