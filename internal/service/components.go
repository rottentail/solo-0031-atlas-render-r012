package service

import (
	"atlas-render/internal/model"
	"atlas-render/internal/scene"
	"fmt"
	"sort"
	"strings"
	"time"
)

// PutComponent publishes an immutable component revision. expected is the
// revision the caller observed; zero creates the component. A disabled
// component accepts no new revision until it is re-enabled, so existing pins
// stay exactly where they are.
func (s *Service) PutComponent(id, name string, expected int, d model.ComponentDocument) (model.Component, error) {
	var out model.Component
	if !scene.ValidID(id) || !scene.TrimmedName(name) || expected < 0 {
		return out, model.Invalid("invalid component id, name or expected revision")
	}
	e := s.Store.Update(func(st *model.State) error {
		old, exists := st.Components[id]
		if exists {
			if old.Revision != expected {
				return model.Conflict("component revision changed")
			}
			if old.State != "active" {
				return model.Conflict("component is disabled")
			}
			if _, ok := scene.ComponentRevision(old, expected+1); ok {
				return model.Conflict("component revision already exists")
			}
		} else if expected != 0 {
			return model.Conflict("component revision changed")
		}
		next := old
		next.ID = id
		next.Name = strings.TrimSpace(name)
		next.Revision = expected + 1
		next.State = "active"
		next.UpdatedAt = time.Now().UTC()
		next.Revisions = append(append([]model.ComponentRevision(nil), old.Revisions...), model.ComponentRevision{Revision: next.Revision, Document: d, CreatedAt: next.UpdatedAt})
		candidate := map[string]model.Component{}
		for k, v := range st.Components {
			candidate[k] = v
		}
		candidate[id] = next
		if e := scene.ValidateComponentRegistry(candidate); e != nil {
			return e
		}
		// Only the newly published revision is checked against live assets.
		// Older revisions survive an asset retirement because frozen snapshots
		// already captured the bytes.
		pins, refs, e := scene.ClosureLayers(d.Layers, candidate)
		if e != nil {
			return e
		}
		for key, p := range pins {
			if c := candidate[p.ID]; c.State == "disabled" {
				return model.Conflictf("component revision references disabled component %s", key)
			}
		}
		for _, ref := range refs {
			a, ok := st.Assets[ref]
			if !ok || a.State != "available" {
				return model.Conflictf("component revision references unavailable asset %s", ref)
			}
		}
		st.Components[id] = next
		if exists {
			st.Emit("component.revised", id)
		} else {
			st.Emit("component.created", id)
		}
		out = next
		return nil
	})
	return out, e
}

// ComponentState enables or disables a component. Disabling is metadata only:
// already frozen snapshots keep rendering their pinned revisions, while new
// pins, adoptions, revisions and freezes are refused.
func (s *Service) ComponentState(id string, expected int, target string) (model.Component, error) {
	var out model.Component
	if target != "active" && target != "disabled" {
		return out, model.Invalid("unknown component state")
	}
	e := s.Store.Update(func(st *model.State) error {
		v, ok := st.Components[id]
		if !ok {
			return model.Missing("component not found")
		}
		if v.Revision != expected {
			return model.Conflict("component revision changed")
		}
		if v.State == target {
			out = v
			return nil
		}
		v.State = target
		v.UpdatedAt = time.Now().UTC()
		st.Components[id] = v
		st.Emit("component."+target, id)
		out = v
		return nil
	})
	return out, e
}

func (s *Service) Component(id string) (model.Component, error) {
	v, ok := s.Store.Read().Components[id]
	if !ok {
		return v, model.Missing("component not found")
	}
	return v, nil
}

// ComponentUsage describes the pins a scene holds. Direct pins are the ones an
// adoption can rewrite; nested occurrences come from instanced component
// revisions and are immutable until the owning component publishes a revision.
type ComponentUsage struct {
	ComponentID       string `json:"componentId"`
	Name              string `json:"name"`
	Revision          int    `json:"revision"`
	CurrentRevision   int    `json:"currentRevision"`
	State             string `json:"state"`
	UpdateAvailable   bool   `json:"updateAvailable"`
	PinnedRevisions   []int  `json:"pinnedRevisions"`
	InstanceCount     int    `json:"instanceCount"`
	NestedRevisions   []int  `json:"nestedRevisions,omitempty"`
	NestedOccurrences int    `json:"nestedOccurrences"`
}

func (s *Service) SceneComponents(id string) ([]ComponentUsage, error) {
	st := s.Store.Read()
	v, ok := st.Scenes[id]
	if !ok {
		return nil, model.Missing("scene not found")
	}
	return componentUsage(v.Document, st.Components), nil
}

func componentUsage(d model.Document, components map[string]model.Component) []ComponentUsage {
	direct := map[string][]int{}
	var walk func([]model.Layer)
	walk = func(ls []model.Layer) {
		for _, l := range ls {
			if l.Kind == "component" {
				direct[l.ComponentID] = append(direct[l.ComponentID], l.ComponentRevision)
			}
			walk(l.Children)
		}
	}
	walk(d.Layers)
	// Nested occurrences may belong to components the scene never instantiates
	// directly; they are still part of its effective content, so report them.
	nested := map[string][]int{}
	for _, key := range scene.NestedPlacements(d.Layers, components) {
		at := strings.LastIndex(key, "@")
		if at < 0 {
			continue
		}
		rev, ok := pinRevision(key)
		if !ok {
			continue
		}
		nested[key[:at]] = append(nested[key[:at]], rev)
	}
	cids := map[string]bool{}
	for cid := range direct {
		cids[cid] = true
	}
	for cid := range nested {
		cids[cid] = true
	}
	out := []ComponentUsage{}
	for cid := range cids {
		revisions := append([]int(nil), direct[cid]...)
		sort.Ints(revisions)
		revs := revisions[:0]
		count := 0
		for _, r := range revisions {
			if len(revs) == 0 || revs[len(revs)-1] != r {
				revs = append(revs, r)
			}
			count++
		}
		u := ComponentUsage{ComponentID: cid, PinnedRevisions: revs, InstanceCount: count}
		if len(revs) > 0 {
			u.Revision = revs[len(revs)-1]
		}
		seenNested := map[int]bool{}
		for _, rev := range nested[cid] {
			if !seenNested[rev] {
				seenNested[rev] = true
				u.NestedRevisions = append(u.NestedRevisions, rev)
			}
			u.NestedOccurrences++
		}
		sort.Ints(u.NestedRevisions)
		if c, ok := components[cid]; ok {
			u.Name = c.Name
			u.CurrentRevision = c.Revision
			u.State = c.State
			stale := false
			if len(revs) > 0 && c.Revision > u.Revision {
				stale = true
			}
			for _, rev := range u.NestedRevisions {
				if c.Revision > rev {
					stale = true
				}
			}
			u.UpdateAvailable = c.State == "active" && stale
		}
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ComponentID < out[j].ComponentID })
	return out
}

// Adopt moves every direct pin of one component to a target revision. The
// scene revision check, the pin rewrite and the adoption record share a single
// transaction, so a scene edited concurrently is either wholly adopted or
// rejected; a mixed document is never published. Pins nested inside component
// revisions are immutable, so an adoption that would leave the effective
// content split across revisions is refused instead of silently mixed.
func (s *Service) Adopt(sceneID, componentID string, expectedScene, target, expectedComponent int) (model.Scene, error) {
	var out model.Scene
	if !scene.ValidID(componentID) || expectedScene < 1 || target < 1 {
		return out, model.Invalid("invalid adoption request")
	}
	e := s.Store.Update(func(st *model.State) error {
		v, ok := st.Scenes[sceneID]
		if !ok {
			return model.Missing("scene not found")
		}
		if v.Revision != expectedScene {
			return model.Conflict("scene revision changed")
		}
		if v.State != "active" {
			return model.Conflict("scene is archived")
		}
		c, ok := st.Components[componentID]
		if !ok {
			return model.Missing("component not found")
		}
		if c.State != "active" {
			return model.Conflict("component is disabled")
		}
		if expectedComponent > 0 && c.Revision != expectedComponent {
			return model.Conflict("component revision changed")
		}
		if _, ok := scene.ComponentRevision(c, target); !ok {
			return model.Conflict("component revision is not available")
		}
		direct := []int{}
		highest := 0
		for _, p := range scene.Pins(v.Document) {
			if p.ID == componentID {
				direct = append(direct, p.Revision)
				if p.Revision > highest {
					highest = p.Revision
				}
			}
		}
		if len(direct) == 0 {
			return model.Conflict("scene does not reference component")
		}
		// Historical revisions stay renderable through snapshots, but a live
		// scene only moves forward: rolling back would quietly resurrect
		// superseded artwork while looking like an ordinary adoption.
		if target < highest {
			return model.Conflict("adoption only moves to a newer revision")
		}
		changed := false
		for _, rev := range direct {
			if rev != target {
				changed = true
			}
		}
		if !changed {
			// Already coherent at the target: adoption is idempotent and does
			// not manufacture a revision.
			out = v
			return nil
		}
		next := scene.RewritePins(v.Document, componentID, target)
		if e := scene.Validate(next, st.Components); e != nil {
			return e
		}
		// Adopting a single component cannot align occurrences baked into
		// another component revision. Refuse rather than publish a scene whose
		// effective content is split across revisions.
		directKeys := map[string]bool{}
		for _, rev := range direct {
			directKeys[scene.PinKey(componentID, rev)] = true
		}
		for _, key := range scene.NestedPlacements(v.Document.Layers, st.Components) {
			if directKeys[key] || key == scene.PinKey(componentID, target) {
				continue
			}
			if strings.HasPrefix(key, componentID+"@") {
				return model.Conflictf("component %s is pinned at %s inside another component revision and cannot be adopted here", componentID, key)
			}
		}
		from := append([]int(nil), direct...)
		sort.Ints(from)
		nextPins, refs, e := scene.Closure(next, st.Components)
		if e != nil {
			return e
		}
		previousPins, _, e := scene.Closure(v.Document, st.Components)
		if e != nil {
			return e
		}
		if disabled := scene.NewDisabledPins(previousPins, nextPins, st.Components); len(disabled) > 0 {
			return model.Conflictf("adopted revision references disabled component %s", disabled[0])
		}
		for _, ref := range refs {
			a, ok := st.Assets[ref]
			if !ok || a.State != "available" {
				return model.Conflict("adopted component references unavailable asset")
			}
		}
		v.Document = next
		v.Revision++
		v.UpdatedAt = time.Now().UTC()
		v.Adoptions = append(append([]model.ComponentAdoption(nil), v.Adoptions...), model.ComponentAdoption{
			ComponentID: componentID,
			From:        from,
			To:          target,
			Revision:    v.Revision,
			At:          v.UpdatedAt,
		})
		st.Scenes[sceneID] = v
		st.Emit("scene.component.adopted", sceneID)
		out = v
		return nil
	})
	return out, e
}

func pinRevision(key string) (int, bool) {
	var v int
	if _, e := fmt.Sscanf(key[strings.LastIndex(key, "@")+1:], "%d", &v); e != nil {
		return 0, false
	}
	return v, true
}
