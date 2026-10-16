package agentapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1", s.hello)
	mux.HandleFunc("GET /v1/projects", s.projects)
	mux.HandleFunc("POST /v1/proposals", s.propose)
	mux.HandleFunc("GET /v1/proposals/{id}", s.proposal)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusNotFound, "no such call; GET /v1 says which API this is")
	})
	return mux
}

func origin(r *http.Request) string {
	o, _ := r.Context().Value(originKey{}).(string)
	return o
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]string{"error": msg})
}

func (s *Server) hello(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	review := s.reviewer != nil
	s.mu.Unlock()
	reply(w, http.StatusOK, map[string]any{
		"api": Version, "origin": origin(r), "laptop": s.opts.Laptop, "interface": review,
	})
}

func (s *Server) projects(w http.ResponseWriter, r *http.Request) {
	all := s.opts.Source.Projects(origin(r))
	if !r.URL.Query().Has("path") {
		if all == nil {
			all = []Project{}
		}
		reply(w, http.StatusOK, map[string]any{"projects": all})
		return
	}
	p := r.URL.Query().Get("path")
	if !path.IsAbs(p) {
		fail(w, http.StatusBadRequest, fmt.Sprintf("path must be absolute, not %q", p))
		return
	}
	found, ok := holding(all, p)
	if !ok {
		fail(w, http.StatusNotFound, fmt.Sprintf("no project holds %s", path.Clean(p)))
		return
	}
	reply(w, http.StatusOK, found)
}

// holding finds the project whose path is p or the nearest parent of it.
func holding(projects []Project, p string) (Project, bool) {
	p = path.Clean(p)
	best, longest := -1, -1
	for i, pr := range projects {
		dir := path.Clean(pr.Path)
		if (p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")) && len(dir) > longest {
			best, longest = i, len(dir)
		}
	}
	if best < 0 {
		return Project{}, false
	}
	return projects[best], true
}

type proposeRequest struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Web  int    `json:"web"`
}

func (s *Server) propose(w http.ResponseWriter, r *http.Request) {
	var req proposeRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := dec.Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(w, http.StatusBadRequest, "the request is over 4 KiB")
			return
		}
		fail(w, http.StatusBadRequest, "the body must be JSON like {\"path\": \"/abs/dir\"}: "+err.Error())
		return
	}
	switch {
	case !path.IsAbs(req.Path):
		fail(w, http.StatusBadRequest, fmt.Sprintf("path must be absolute, not %q", req.Path))
		return
	case req.Web < 0 || req.Web > 65535:
		fail(w, http.StatusBadRequest, fmt.Sprintf("web must be a port between 1 and 65535, not %d", req.Web))
		return
	case !plainName(req.Name):
		fail(w, http.StatusBadRequest, "name must be at most 64 printable characters on one line")
		return
	}
	o := origin(r)
	if found, ok := holding(s.opts.Source.Projects(o), req.Path); ok {
		reply(w, http.StatusOK, map[string]any{"state": StateExists, "project": found})
		return
	}

	s.mu.Lock()
	s.sweepLocked()
	reviewer := s.reviewer
	pending := 0
	for _, p := range s.proposals {
		if p.Origin == o && p.state == StatePending {
			pending++
		}
	}
	if reviewer == nil {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "open nearhost on "+s.laptop()+" to review proposals; `nearhost up` cannot show them")
		return
	}
	if pending >= maxPending {
		s.mu.Unlock()
		fail(w, http.StatusTooManyRequests, fmt.Sprintf("%d proposals are already waiting for review; wait for them first", pending))
		return
	}
	p := &proposal{
		Proposal: Proposal{
			ID: newID(), Origin: o, Path: path.Clean(req.Path), Name: req.Name, Web: req.Web,
			Expires: s.opts.Now().Add(expiry),
		},
		state: StatePending,
		done:  make(chan struct{}),
	}
	s.proposals[p.ID] = p
	s.mu.Unlock()
	reviewer.Review(p.Proposal)
	reply(w, http.StatusAccepted, map[string]any{"id": p.ID, "expires_at": p.Expires.UTC().Format(time.RFC3339)})
}

func (s *Server) laptop() string {
	if s.opts.Laptop == "" {
		return "your laptop"
	}
	return s.opts.Laptop
}

// plainName reports whether a proposed name could be shown in the form: at
// most 64 printable characters on one line. The form checks the rest.
func plainName(name string) bool {
	if len([]rune(name)) > 64 {
		return false
	}
	for _, c := range name {
		if !unicode.IsPrint(c) {
			return false
		}
	}
	return true
}

func (s *Server) proposal(w http.ResponseWriter, r *http.Request) {
	wait := time.Duration(0)
	if v := r.URL.Query().Get("wait"); v != "" {
		n, err := strconv.Atoi(strings.TrimSuffix(v, "s"))
		if err != nil || n < 0 {
			fail(w, http.StatusBadRequest, fmt.Sprintf("wait must be a number of seconds, not %q", v))
			return
		}
		wait = min(time.Duration(n)*time.Second, maxWait)
	}
	o := origin(r)
	s.mu.Lock()
	s.sweepLocked()
	p := s.proposals[r.PathValue("id")]
	if p == nil || p.Origin != o {
		s.mu.Unlock()
		fail(w, http.StatusNotFound, "no such proposal; it may have ended over 10 minutes ago")
		return
	}
	s.expireLocked(p)
	done, left := p.done, p.Expires.Sub(s.opts.Now())
	s.mu.Unlock()

	if wait > 0 {
		t := time.NewTimer(min(wait, max(left, 0)))
		select {
		case <-done:
		case <-t.C:
		case <-r.Context().Done():
			t.Stop()
			return
		}
		t.Stop()
	}

	s.mu.Lock()
	s.expireLocked(p)
	state, reason, name := p.state, p.reason, p.project
	s.mu.Unlock()
	out := map[string]any{"state": state}
	if reason != "" {
		out["reason"] = reason
	}
	if state == StateAccepted {
		project := Project{Name: name}
		for _, pr := range s.opts.Source.Projects(o) {
			if pr.Name == name {
				project = pr
			}
		}
		out["project"] = project
	}
	reply(w, http.StatusOK, out)
}
