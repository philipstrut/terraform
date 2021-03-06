package terraform

import (
	"fmt"

	"github.com/hashicorp/terraform/config"
	"github.com/hashicorp/terraform/dag"
)

// GraphNodeReferenceable must be implemented by any node that represents
// a Terraform thing that can be referenced (resource, module, etc.).
type GraphNodeReferenceable interface {
	// ReferenceableName is the name by which this can be referenced.
	// This can be either just the type, or include the field. Example:
	// "aws_instance.bar" or "aws_instance.bar.id".
	ReferenceableName() []string
}

// GraphNodeReferencer must be implemented by nodes that reference other
// Terraform items and therefore depend on them.
type GraphNodeReferencer interface {
	// References are the list of things that this node references. This
	// can include fields or just the type, just like GraphNodeReferenceable
	// above.
	References() []string
}

// GraphNodeReferenceGlobal is an interface that can optionally be
// implemented. If ReferenceGlobal returns true, then the References()
// and ReferenceableName() must be _fully qualified_ with "module.foo.bar"
// etc.
//
// This allows a node to reference and be referenced by a specific name
// that may cross module boundaries. This can be very dangerous so use
// this wisely.
//
// The primary use case for this is module boundaries (variables coming in).
type GraphNodeReferenceGlobal interface {
	// Set to true to signal that references and name are fully
	// qualified. See the above docs for more information.
	ReferenceGlobal() bool
}

// ReferenceTransformer is a GraphTransformer that connects all the
// nodes that reference each other in order to form the proper ordering.
type ReferenceTransformer struct{}

func (t *ReferenceTransformer) Transform(g *Graph) error {
	// Build a reference map so we can efficiently look up the references
	vs := g.Vertices()
	m := NewReferenceMap(vs)

	// Find the things that reference things and connect them
	for _, v := range vs {
		parents, _ := m.References(v)
		for _, parent := range parents {
			g.Connect(dag.BasicEdge(v, parent))
		}
	}

	return nil
}

// ReferenceMap is a structure that can be used to efficiently check
// for references on a graph.
type ReferenceMap struct {
	// m is the mapping of referenceable name to list of verticies that
	// implement that name. This is built on initialization.
	m map[string][]dag.Vertex
}

// References returns the list of vertices that this vertex
// references along with any missing references.
func (m *ReferenceMap) References(v dag.Vertex) ([]dag.Vertex, []string) {
	rn, ok := v.(GraphNodeReferencer)
	if !ok {
		return nil, nil
	}

	var matches []dag.Vertex
	var missing []string
	prefix := m.prefix(v)
	for _, n := range rn.References() {
		n = prefix + n
		parents, ok := m.m[n]
		if !ok {
			missing = append(missing, n)
			continue
		}

		// Make sure this isn't a self reference, which isn't included
		selfRef := false
		for _, p := range parents {
			if p == v {
				selfRef = true
				break
			}
		}
		if selfRef {
			continue
		}

		matches = append(matches, parents...)
	}

	return matches, missing
}

func (m *ReferenceMap) prefix(v dag.Vertex) string {
	// If the node is stating it is already fully qualified then
	// we don't have to create the prefix!
	if gn, ok := v.(GraphNodeReferenceGlobal); ok && gn.ReferenceGlobal() {
		return ""
	}

	// Create the prefix based on the path
	var prefix string
	if pn, ok := v.(GraphNodeSubPath); ok {
		if path := normalizeModulePath(pn.Path()); len(path) > 1 {
			prefix = modulePrefixStr(path) + "."
		}
	}

	return prefix
}

// NewReferenceMap is used to create a new reference map for the
// given set of vertices.
func NewReferenceMap(vs []dag.Vertex) *ReferenceMap {
	var m ReferenceMap

	// Build the lookup table
	refMap := make(map[string][]dag.Vertex)
	for _, v := range vs {
		// We're only looking for referenceable nodes
		rn, ok := v.(GraphNodeReferenceable)
		if !ok {
			continue
		}

		// Go through and cache them
		prefix := m.prefix(v)
		for _, n := range rn.ReferenceableName() {
			n = prefix + n
			refMap[n] = append(refMap[n], v)
		}
	}

	m.m = refMap
	return &m
}

// ReferencesFromConfig returns the references that a configuration has
// based on the interpolated variables in a configuration.
func ReferencesFromConfig(c *config.RawConfig) []string {
	var result []string
	for _, v := range c.Variables {
		if r := ReferenceFromInterpolatedVar(v); r != "" {
			result = append(result, r)
		}

	}

	return result
}

// ReferenceFromInterpolatedVar returns the reference from this variable,
// or an empty string if there is no reference.
func ReferenceFromInterpolatedVar(v config.InterpolatedVariable) string {
	switch v := v.(type) {
	case *config.ModuleVariable:
		return fmt.Sprintf("module.%s.output.%s", v.Name, v.Field)
	case *config.ResourceVariable:
		return v.ResourceId()
	case *config.UserVariable:
		return fmt.Sprintf("var.%s", v.Name)
	default:
		return ""
	}
}
