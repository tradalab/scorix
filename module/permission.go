package module

import "slices"

// A Permission names something the app may do, not the component that does it.
// One module serves several kinds of request - listing models and deleting three
// gigabytes of them are not one grant - and two modules can serve the same kind,
// the way browser, dialog and updater all speak for "shell".
type Permission string

// What a module lets an app grant: Atoms are the finest names its handlers ask
// for, Sets are the names a manifest actually writes. A set named after the
// module's capability is what keeps manifests written before permissions
// existed granting the same thing.
type PermissionSet struct {
	Atoms []Permission
	Sets  map[Permission][]Permission
}

// Optional: a module that splits its surface. Without it every handler needs the
// module's one capability.
type Permissioned interface {
	Permissions() PermissionSet
}

// Every name an app may write for this module.
func (p PermissionSet) Names() []Permission {
	out := slices.Clone(p.Atoms)
	for name := range p.Sets {
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// The atoms a granted name stands for; an atom stands for itself.
func (p PermissionSet) Expand(name Permission) []Permission {
	if members, ok := p.Sets[name]; ok {
		return members
	}
	if slices.Contains(p.Atoms, name) {
		return []Permission{name}
	}
	return nil
}

// Refuses a set naming an atom the module never declared: the app would grant
// that set and the handler would still be denied, both halves looking right.
func (p PermissionSet) Validate() error {
	for name, members := range p.Sets {
		for _, m := range members {
			if !slices.Contains(p.Atoms, m) {
				return &permissionError{set: name, atom: m}
			}
		}
	}
	return nil
}

type permissionError struct {
	set  Permission
	atom Permission
}

func (e *permissionError) Error() string {
	return "permission set " + string(e.set) + " names " + string(e.atom) + ", which is not one of the module's atoms"
}
