package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// PipelineService is the server-held pipeline surface. It is optional: a
// deployment that never wires it serves no pipeline endpoints at all, rather
// than serving endpoints that answer "unavailable" to every request.
//
// The responses are typed `any` because internal/service imports this package,
// so this package cannot name its types.
type PipelineService interface {
	// PipelineShow takes the repository and the trigger word.
	PipelineShow(context.Context, string, string) (any, error)
	// PipelineSet takes the acting identity, repository, trigger word, the
	// document bytes, and the commit to fingerprint (empty for the default
	// branch head).
	PipelineSet(context.Context, string, string, string, []byte, string) (any, error)
	// PipelineUnset takes the acting identity, repository and trigger word.
	PipelineUnset(context.Context, string, string, string) (any, error)
	// PipelineCheck takes the acting identity, repository, trigger word, the
	// commit to regenerate from, and whether to store the result.
	PipelineCheck(context.Context, string, string, string, string, bool) (any, error)
	// RepoRegister takes the acting identity, the repository name, and the
	// upstream name (empty to take the only one). It is idempotent.
	RepoRegister(context.Context, string, string, string) (any, error)
}

// WithPipelines installs the server-held pipeline endpoints.
func WithPipelines(pipelines PipelineService) func(*Server) {
	return func(server *Server) { server.pipelines = pipelines }
}

// maxPipelineDocumentBytes bounds a stored document at the edge, so an
// oversized body is refused before it is buffered rather than after.
const maxPipelineDocumentBytes = 1 << 20

// pipelineSetRequest is the PUT body. The repository travels in the body
// rather than in the path because an Oberth repository name is qualified with
// slashes ("upstream/org/repo") and a path segment cannot carry it.
type pipelineSetRequest struct {
	Repo     string `json:"repo"`
	Trigger  string `json:"trigger"`
	Document string `json:"document"`
	Ref      string `json:"ref"`
}

// repoRegisterRequest is the POST /api/repos body. The repository travels in
// the body for the same reason it does on the pipeline endpoints: an Oberth
// repository name is qualified with slashes and a path segment cannot carry
// it.
type repoRegisterRequest struct {
	Repo     string `json:"repo"`
	Upstream string `json:"upstream"`
}

type pipelineCheckRequest struct {
	Repo    string `json:"repo"`
	Trigger string `json:"trigger"`
	Ref     string `json:"ref"`
	Store   bool   `json:"store"`
}

func (server *Server) handlePipelineShow(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	repo := strings.TrimSpace(query.Get("repo"))
	if repo == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "a repo query parameter is required"})
		return
	}
	value, err := server.pipelines.PipelineShow(request.Context(), repo, query.Get("trigger"))
	server.writeView(writer, value, err)
}

func (server *Server) handlePipelineSet(writer http.ResponseWriter, request *http.Request) {
	var body pipelineSetRequest
	if !decodePipelineBody(writer, request, &body) {
		return
	}
	if strings.TrimSpace(body.Repo) == "" || strings.TrimSpace(body.Document) == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "repo and document are required"})
		return
	}
	actor := actorFrom(request.Context())
	value, err := server.pipelines.PipelineSet(request.Context(), actor.Identity,
		body.Repo, body.Trigger, []byte(body.Document), body.Ref)
	server.writeView(writer, value, err)
}

func (server *Server) handlePipelineUnset(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	repo := strings.TrimSpace(query.Get("repo"))
	if repo == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "a repo query parameter is required"})
		return
	}
	actor := actorFrom(request.Context())
	value, err := server.pipelines.PipelineUnset(request.Context(), actor.Identity, repo, query.Get("trigger"))
	server.writeView(writer, value, err)
}

func (server *Server) handlePipelineCheck(writer http.ResponseWriter, request *http.Request) {
	var body pipelineCheckRequest
	if !decodePipelineBody(writer, request, &body) {
		return
	}
	if strings.TrimSpace(body.Repo) == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "repo is required"})
		return
	}
	actor := actorFrom(request.Context())
	value, err := server.pipelines.PipelineCheck(request.Context(), actor.Identity,
		body.Repo, body.Trigger, body.Ref, body.Store)
	server.writeView(writer, value, err)
}

// handleRepoRegister is registration over the API.
//
// It exists so that onboarding a repository does not require `kubectl exec`
// into the server pod, which is what made onboarding an administrator's task
// rather than a one-line command.
func (server *Server) handleRepoRegister(writer http.ResponseWriter, request *http.Request) {
	var body repoRegisterRequest
	if !decodePipelineBody(writer, request, &body) {
		return
	}
	if strings.TrimSpace(body.Repo) == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "repo is required"})
		return
	}
	actor := actorFrom(request.Context())
	value, err := server.pipelines.RepoRegister(request.Context(), actor.Identity, body.Repo, body.Upstream)
	server.writeView(writer, value, err)
}

func decodePipelineBody(writer http.ResponseWriter, request *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxPipelineDocumentBytes+maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return false
	}
	return true
}
