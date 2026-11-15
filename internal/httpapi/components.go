package httpapi

import (
	"atlas-render/internal/model"
	"net/http"
)

func (a API) putComponent(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name     string                  `json:"name"`
		Expected int                     `json:"expectedRevision"`
		Document model.ComponentDocument `json:"document"`
	}
	if e := decode(w, r, &b); e != nil {
		fail(w, e)
		return
	}
	v, e := a.Service.PutComponent(r.PathValue("id"), b.Name, b.Expected, b.Document)
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, v)
}
func (a API) getComponent(w http.ResponseWriter, r *http.Request) {
	v, e := a.Service.Component(r.PathValue("id"))
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, v)
}
func (a API) componentState(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Expected int    `json:"expectedRevision"`
		State    string `json:"state"`
	}
	if e := decode(w, r, &b); e != nil {
		fail(w, e)
		return
	}
	v, e := a.Service.ComponentState(r.PathValue("id"), b.Expected, b.State)
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, v)
}
func (a API) sceneComponents(w http.ResponseWriter, r *http.Request) {
	v, e := a.Service.SceneComponents(r.PathValue("id"))
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, v)
}
func (a API) adoptComponent(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Expected      int `json:"expectedRevision"`
		Revision      int `json:"revision"`
		ComponentFrom int `json:"expectedComponentRevision"`
	}
	if e := decode(w, r, &b); e != nil {
		fail(w, e)
		return
	}
	v, e := a.Service.Adopt(r.PathValue("id"), r.PathValue("component"), b.Expected, b.Revision, b.ComponentFrom)
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, v)
}
