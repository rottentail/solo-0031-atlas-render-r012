package scene

import (
	"atlas-render/internal/model"
	"atlas-render/internal/paint"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

const (
	MaxLayers = 200
	MaxDepth  = 8
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)

func ValidID(id string) bool { return identifier.MatchString(id) }
func finite(v float64) bool  { return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) <= 100000 }

// PinKey names one immutable component revision. Scenes store pins, not floats
// of "latest", so an updated component never silently changes frozen content.
func PinKey(id string, revision int) string { return fmt.Sprintf("%s@%d", id, revision) }

// ComponentRevision resolves the immutable revision pointed at by a pin.
func ComponentRevision(c model.Component, revision int) (model.ComponentRevision, bool) {
	for _, v := range c.Revisions {
		if v.Revision == revision {
			return v, true
		}
	}
	return model.ComponentRevision{}, false
}

func TrimmedName(name string) bool {
	n := strings.TrimSpace(name)
	return n != "" && len(n) <= 160
}

// Validate checks a scene document against the current component registry.
// Component revisions are immutable once written; only the pin may move.
func Validate(d model.Document, components map[string]model.Component) error {
	if d.Width < 1 || d.Height < 1 || d.Width > 1536 || d.Height > 1536 {
		return model.Invalid("canvas dimensions must be 1..1536")
	}
	if _, e := paint.Parse(d.Background); e != nil {
		return model.Invalid("background: %v", e)
	}
	v := &validator{components: components}
	if e := v.walk(d.Layers, 0, map[string]bool{}); e != nil {
		return e
	}
	_, _, e := ClosureLayers(d.Layers, components)
	return e
}

// ValidateComponentDocument checks a component revision body against a registry
// that must already contain the revision being checked, so self reference is
// visible as a cycle.
func ValidateComponentDocument(d model.ComponentDocument, components map[string]model.Component) error {
	v := &validator{components: components}
	if e := v.walk(d.Layers, 0, map[string]bool{}); e != nil {
		return e
	}
	_, _, e := ClosureLayers(d.Layers, components)
	return e
}

// ValidateComponentRegistry is used when persisted state is loaded or committed.
func ValidateComponentRegistry(components map[string]model.Component) error {
	for id, c := range components {
		if id != c.ID || !ValidID(id) {
			return fmt.Errorf("invalid component record %s", id)
		}
		if !TrimmedName(c.Name) {
			return fmt.Errorf("component %s has invalid name", id)
		}
		if c.State != "active" && c.State != "disabled" {
			return fmt.Errorf("component %s has invalid state", id)
		}
		if c.Revision < 1 || len(c.Revisions) == 0 {
			return fmt.Errorf("component %s has no revisions", id)
		}
		seen := map[int]bool{}
		highest := 0
		for _, v := range c.Revisions {
			if v.Revision < 1 || seen[v.Revision] {
				return fmt.Errorf("component %s has invalid revision", id)
			}
			seen[v.Revision] = true
			if v.Revision > highest {
				highest = v.Revision
			}
			if e := ValidateComponentDocument(v.Document, components); e != nil {
				return fmt.Errorf("component %s revision %d: %w", id, v.Revision, e)
			}
			// Immutable revisions resolve only through this registry, never
			// through live asset records; retirement of an asset is a problem
			// only for newly published revisions, which the service checks.
		}
		if highest != c.Revision {
			return fmt.Errorf("component %s current revision is not the highest", id)
		}
	}
	return nil
}

type validator struct {
	components map[string]model.Component
}

// walk checks only the literal document tree. Component bodies are validated
// once per immutable revision, so their ids may legitimately repeat across
// multiple instances of the same component.
func (v *validator) walk(layers []model.Layer, depth int, seen map[string]bool) error {
	if depth > MaxDepth {
		return model.Invalid("scene nesting exceeds eight")
	}
	for _, l := range layers {
		if !ValidID(l.ID) || seen[l.ID] {
			return model.Invalid("layer id invalid or repeated: %s", l.ID)
		}
		seen[l.ID] = true
		t := l.Transform
		for _, n := range []float64{l.Width, l.Height, l.Opacity, t.X, t.Y, t.ScaleX, t.ScaleY, t.Degrees} {
			if !finite(n) {
				return model.Invalid("layer %s: nonfinite or unbounded geometry", l.ID)
			}
		}
		if l.Opacity < 0 || l.Opacity > 1 || math.Abs(t.ScaleX) < .001 || math.Abs(t.ScaleY) < .001 || math.Abs(t.ScaleX) > 100 || math.Abs(t.ScaleY) > 100 {
			return model.Invalid("layer %s: invalid opacity or scale", l.ID)
		}
		if l.Clip != nil {
			r := l.Clip
			if !finite(r.X) || !finite(r.Y) || !finite(r.Width) || !finite(r.Height) || r.Width <= 0 || r.Height <= 0 {
				return model.Invalid("layer %s: invalid clip", l.ID)
			}
		}
		switch l.Kind {
		case "group":
			if len(l.Children) == 0 {
				return model.Invalid("group %s is empty", l.ID)
			}
			if e := v.walk(l.Children, depth+1, seen); e != nil {
				return e
			}
			continue
		case "component":
			if len(l.Children) > 0 || l.AssetID != "" || l.ComponentID == "" {
				return model.Invalid("component instance %s must reference a component and no children", l.ID)
			}
			if !ValidID(l.ComponentID) || l.ComponentRevision < 1 {
				return model.Invalid("component instance %s has invalid pin", l.ID)
			}
			c, ok := v.components[l.ComponentID]
			if !ok {
				return model.Conflictf("component %s is not available", l.ComponentID)
			}
			if _, ok := ComponentRevision(c, l.ComponentRevision); !ok {
				return model.Conflictf("component %s revision %d is not available", l.ComponentID, l.ComponentRevision)
			}
			continue
		}
		if len(l.Children) > 0 {
			return model.Invalid("only groups can contain layers")
		}
		if l.Width <= 0 || l.Height <= 0 || l.Width > 4096 || l.Height > 4096 {
			return model.Invalid("layer %s: invalid dimensions", l.ID)
		}
		switch l.Kind {
		case "rectangle", "ellipse":
		case "polygon":
			if len(l.Points) < 3 || len(l.Points) > 128 {
				return model.Invalid("polygon requires 3..128 points")
			}
			for _, p := range l.Points {
				if !finite(p.X) || !finite(p.Y) || p.X < 0 || p.X > l.Width || p.Y < 0 || p.Y > l.Height {
					return model.Invalid("polygon point outside local bounds")
				}
			}
		case "image":
			if !ValidID(l.AssetID) {
				return model.Invalid("image requires asset id")
			}
			continue
		default:
			return model.Invalid("layer %s: unknown kind", l.ID)
		}
		if l.Fill.Kind != "solid" && l.Fill.Kind != "linear" {
			return model.Invalid("unknown paint kind")
		}
		if _, e := paint.Parse(l.Fill.Color); e != nil {
			return model.Invalid("invalid fill color")
		}
		if l.Fill.Kind == "linear" {
			if _, e := paint.Parse(l.Fill.EndColor); e != nil {
				return model.Invalid("invalid gradient color")
			}
			for _, n := range []float64{l.Fill.From.X, l.Fill.From.Y, l.Fill.To.X, l.Fill.To.Y} {
				if !finite(n) {
					return model.Invalid("invalid gradient endpoint")
				}
			}
			if l.Fill.From == l.Fill.To {
				return model.Invalid("gradient endpoints coincide")
			}
		}
	}
	return nil
}

// References lists the asset ids used by the expanded document, including
// assets owned by component revisions.
func References(d model.Document, components map[string]model.Component) ([]string, error) {
	_, refs, e := Closure(d, components)
	return refs, e
}

// LegacyReferences is the literal-tree walk used only for documents that carry
// no component pins.
func LegacyReferences(d model.Document) []string {
	seen := map[string]bool{}
	out := []string{}
	var walk func([]model.Layer)
	walk = func(ls []model.Layer) {
		for _, l := range ls {
			if l.Kind == "image" && !seen[l.AssetID] {
				seen[l.AssetID] = true
				out = append(out, l.AssetID)
			}
			walk(l.Children)
		}
	}
	walk(d.Layers)
	return out
}

// Pins lists the component instances directly present in a scene document.
// Pins inside a component revision belong to that immutable revision and are
// not rewritten by scene-level adoption.
func Pins(d model.Document) []model.ComponentTarget {
	seen := map[string]bool{}
	out := []model.ComponentTarget{}
	var walk func([]model.Layer)
	walk = func(ls []model.Layer) {
		for _, l := range ls {
			if l.Kind == "component" {
				key := PinKey(l.ComponentID, l.ComponentRevision)
				if !seen[key] {
					seen[key] = true
					out = append(out, model.ComponentTarget{ID: l.ComponentID, Revision: l.ComponentRevision})
				}
			}
			walk(l.Children)
		}
	}
	walk(d.Layers)
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID == out[j].ID {
			return out[i].Revision < out[j].Revision
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// NestedPlacements lists component pins that occur inside a component revision
// reachable from the document. They are structurally distinct from direct pins
// (which live in the scene tree) even when both point at the same revision:
// only direct pins can be moved by a scene, nested ones belong to an immutable
// component revision.
func NestedPlacements(layers []model.Layer, components map[string]model.Component) []string {
	out := []string{}
	var stack []string
	var walk func([]model.Layer, bool) error
	walk = func(ls []model.Layer, inside bool) error {
		for _, l := range ls {
			if l.Kind == "component" {
				key := PinKey(l.ComponentID, l.ComponentRevision)
				if inside {
					out = append(out, key)
				}
				c, ok := components[l.ComponentID]
				if !ok {
					continue
				}
				rev, ok := ComponentRevision(c, l.ComponentRevision)
				if !ok {
					continue
				}
				skip := false
				for _, active := range stack {
					if active == key {
						skip = true
					}
				}
				if !skip {
					stack = append(stack, key)
					e := walk(rev.Document.Layers, true)
					stack = stack[:len(stack)-1]
					if e != nil {
						return e
					}
				}
				continue
			}
			if e := walk(l.Children, inside); e != nil {
				return e
			}
		}
		return nil
	}
	if e := walk(layers, false); e != nil {
		return nil
	}
	return out
}

// RewritePins returns the document with every direct instance of component id
// moved to revision. Pins inside component revisions are immutable and belong
// to that revision, not to the scene adopting it.
func RewritePins(d model.Document, id string, revision int) model.Document {
	var walk func([]model.Layer) []model.Layer
	walk = func(ls []model.Layer) []model.Layer {
		out := make([]model.Layer, len(ls))
		for i, l := range ls {
			if l.Kind == "component" && l.ComponentID == id {
				l.ComponentRevision = revision
			}
			l.Children = walk(l.Children)
			out[i] = l
		}
		return out
	}
	out := d
	out.Layers = walk(d.Layers)
	return out
}

// Closure resolves every component pin reachable from a document and lists the
// asset ids referenced by the expanded result. Expansion is bounded by the same
// layer and depth limits as a hand-written scene, and cycles are rejected with
// the chain that produced them.
func Closure(d model.Document, components map[string]model.Component) (map[string]model.PinnedComponent, []string, error) {
	return ClosureLayers(d.Layers, components)
}

func ClosureLayers(layers []model.Layer, components map[string]model.Component) (map[string]model.PinnedComponent, []string, error) {
	pinned := map[string]model.PinnedComponent{}
	assets := map[string]bool{}
	var stack []string
	count := 0
	var walk func([]model.Layer, int) error
	walk = func(ls []model.Layer, depth int) error {
		if depth > MaxDepth {
			return model.Invalid("expanded scene nesting exceeds eight")
		}
		for _, l := range ls {
			count++
			if count > MaxLayers {
				return model.Invalid("expanded component scene exceeds %d layers", MaxLayers)
			}
			if l.Kind == "image" {
				assets[l.AssetID] = true
			}
			if l.Kind != "component" {
				if e := walk(l.Children, depth+1); e != nil {
					return e
				}
				continue
			}
			key := PinKey(l.ComponentID, l.ComponentRevision)
			for _, active := range stack {
				if active == key {
					return model.Invalid("component cycle: %s", strings.Join(append(append([]string{}, stack...), key), " -> "))
				}
			}
			c, ok := components[l.ComponentID]
			if !ok {
				return model.Conflictf("component %s is not available", l.ComponentID)
			}
			rev, ok := ComponentRevision(c, l.ComponentRevision)
			if !ok {
				return model.Conflictf("component %s revision %d is not available", l.ComponentID, l.ComponentRevision)
			}
			pinned[key] = model.PinnedComponent{ID: c.ID, Revision: rev.Revision, Name: c.Name, Document: rev.Document}
			stack = append(stack, key)
			e := walk(rev.Document.Layers, depth+1)
			stack = stack[:len(stack)-1]
			if e != nil {
				return e
			}
		}
		return nil
	}
	if e := walk(layers, 0); e != nil {
		return nil, nil, e
	}
	out := make([]string, 0, len(assets))
	for id := range assets {
		out = append(out, id)
	}
	sort.Strings(out)
	return pinned, out, nil
}

// DisabledPins reports which of the given pins currently point at disabled
// components. State is a record property, so a pin can become disabled without
// its frozen content changing.
func DisabledPins(components map[string]model.Component, pins map[string]model.PinnedComponent) []string {
	out := []string{}
	for _, p := range pins {
		if c, ok := components[p.ID]; ok && c.State == "disabled" {
			out = append(out, PinKey(p.ID, p.Revision))
		}
	}
	sort.Strings(out)
	return out
}

// NewDisabledPins reports pins in next that reach a disabled component and were
// not already part of previous. Editing a scene may keep the pins it already
// held, but a disabled component cannot be newly introduced, directly or
// through a component revision that embeds it.
func NewDisabledPins(previous, next map[string]model.PinnedComponent, components map[string]model.Component) []string {
	out := []string{}
	for key, p := range next {
		if _, ok := previous[key]; ok {
			continue
		}
		if c, ok := components[p.ID]; ok && c.State == "disabled" {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// FromPinned reconstructs a lookup registry from a frozen snapshot closure. A
// component may be pinned at more than one revision by the same scene, so all
// pinned revisions are retained.
func FromPinned(pins map[string]model.PinnedComponent) map[string]model.Component {
	out := map[string]model.Component{}
	for _, p := range pins {
		c := out[p.ID]
		c.ID = p.ID
		c.Name = p.Name
		c.State = "active"
		c.Revisions = append(c.Revisions, model.ComponentRevision{Revision: p.Revision, Document: p.Document})
		if p.Revision > c.Revision {
			c.Revision = p.Revision
		}
		out[p.ID] = c
	}
	return out
}
