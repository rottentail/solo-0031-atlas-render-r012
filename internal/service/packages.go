package service

import (
	"atlas-render/internal/bundle"
	"atlas-render/internal/model"
	"atlas-render/internal/scene"
	"time"
)

func (s *Service) Export(id string) (bundle.Package, error) {
	v, ok := s.Store.Read().Snapshots[id]
	if !ok {
		return bundle.Package{}, model.Missing("snapshot not found")
	}
	return bundle.Export(v), nil
}

// Import atomically installs the package's scene, assets and component pins.
// A package carries the complete closure; the destination must not already
// use the same component id for different content or a different revision.
func (s *Service) Import(id, name string, p bundle.Package) (model.Scene, error) {
	var out model.Scene
	if !scene.ValidID(id) || !scene.TrimmedName(name) {
		return out, model.Invalid("invalid import destination")
	}
	if e := bundle.Validate(p); e != nil {
		return out, e
	}
	e := s.Store.Update(func(st *model.State) error {
		if _, ok := st.Scenes[id]; ok {
			return model.Conflict("destination scene already exists")
		}
		for aid, a := range p.Snapshot.Assets {
			if existing, ok := st.Assets[aid]; ok {
				if existing.Digest != a.Digest || existing.State != "available" {
					return model.Conflict("destination asset conflicts with package")
				}
			} else {
				a.State = "available"
				st.Assets[aid] = a
			}
		}
		for _, p := range p.Snapshot.Components {
			existing, ok := st.Components[p.ID]
			if !ok {
				st.Components[p.ID] = model.Component{ID: p.ID, Name: p.Name, Revision: p.Revision, State: "active", Revisions: []model.ComponentRevision{{Revision: p.Revision, Document: p.Document, CreatedAt: time.Now().UTC()}}, UpdatedAt: time.Now().UTC()}
				continue
			}
			if existing.State != "active" {
				return model.Conflictf("destination component %s is disabled", p.ID)
			}
			rev, ok := scene.ComponentRevision(existing, p.Revision)
			if !ok || model.CanonicalDigest(rev.Document) != model.CanonicalDigest(p.Document) {
				return model.Conflict("destination component conflicts with package")
			}
		}
		if e := scene.Validate(p.Snapshot.Document, st.Components); e != nil {
			return e
		}
		out = model.Scene{ID: id, Name: name, Revision: 1, State: "active", Document: p.Snapshot.Document, UpdatedAt: time.Now().UTC()}
		st.Scenes[id] = out
		st.Emit("scene.imported", id)
		return nil
	})
	return out, e
}
