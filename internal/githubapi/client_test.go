package githubapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()

	client, err := NewClient("token", "o", "r", baseURL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return client
}

func TestNewClientRejectsInvalidBaseURL(t *testing.T) {
	if _, err := NewClient("token", "o", "r", "not-a-url"); err == nil {
		t.Fatal("expected an error for a hostless base URL")
	}
}

func TestPullRequestMapsHead(t *testing.T) {
	var path string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path

		_, _ = io.WriteString(w, `{"state":"open","merged":false,"head":{"sha":"abcdef1234567890","ref":"floor-test-7"}}`)
	}))
	defer server.Close()

	pr, err := newTestClient(t, server.URL).PullRequest(context.Background(), 7)
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}

	if path != "/repos/o/r/pulls/7" {
		t.Errorf("path = %q, want %q", path, "/repos/o/r/pulls/7")
	}

	if pr.HeadSHA != "abcdef1234567890" || pr.HeadRef != "floor-test-7" || pr.State != "open" || pr.Merged {
		t.Fatalf("unexpected PR %+v", pr)
	}
}

func TestUpsertCommentEditsExisting(t *testing.T) {
	var (
		listPath string
		editPath string
		edited   string
		created  bool
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listPath = r.URL.Path

			_, _ = io.WriteString(w, `[{"id":1,"body":"unrelated"},{"id":2,"body":"<!-- floor:sandbox -->\nold"}]`)
		case http.MethodPatch:
			editPath = r.URL.Path

			var body struct {
				Body string `json:"body"`
			}

			_ = json.NewDecoder(r.Body).Decode(&body)

			edited = body.Body

			_, _ = io.WriteString(w, `{"id":2}`)
		case http.MethodPost:
			created = true

			_, _ = io.WriteString(w, `{"id":3}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	if err := newTestClient(t, server.URL).UpsertComment(context.Background(), 7, "<!-- floor:sandbox -->", "<!-- floor:sandbox -->\nnew"); err != nil {
		t.Fatalf("UpsertComment: %v", err)
	}

	if created {
		t.Error("expected edit, got create")
	}

	if listPath != "/repos/o/r/issues/7/comments" {
		t.Errorf("list path = %q, want %q", listPath, "/repos/o/r/issues/7/comments")
	}

	if editPath != "/repos/o/r/issues/comments/2" {
		t.Errorf("edit path = %q, want %q", editPath, "/repos/o/r/issues/comments/2")
	}

	if !strings.Contains(edited, "new") {
		t.Fatalf("unexpected edited body %q", edited)
	}
}

func TestUpsertCommentCreatesWhenAbsent(t *testing.T) {
	var (
		createPath string
		created    string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, `[{"id":1,"body":"unrelated"}]`)
		case http.MethodPost:
			createPath = r.URL.Path

			var body struct {
				Body string `json:"body"`
			}

			_ = json.NewDecoder(r.Body).Decode(&body)

			created = body.Body

			_, _ = io.WriteString(w, `{"id":3}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	if err := newTestClient(t, server.URL).UpsertComment(context.Background(), 7, "<!-- floor:sandbox -->", "<!-- floor:sandbox -->\nnew"); err != nil {
		t.Fatalf("UpsertComment: %v", err)
	}

	if createPath != "/repos/o/r/issues/7/comments" {
		t.Errorf("create path = %q, want %q", createPath, "/repos/o/r/issues/7/comments")
	}

	if !strings.Contains(created, "<!-- floor:sandbox -->") {
		t.Fatalf("created comment missing marker: %q", created)
	}
}
