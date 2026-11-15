package sandbox

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/osinfra-io/pt-techne-floor/internal/floor"
	"github.com/osinfra-io/pt-techne-floor/internal/githubapi"
)

const (
	testNamespace     = "pt-techne-floor-fixture"
	testGitRepository = "fixture"
	testPrimary       = "regional-sandbox"
	testPlanner       = "regional-sandbox-pr-7"
	testPlanID        = "plan-floor-test-7-abcdef1234"
	testHeadRef       = "floor-test-7"
	testHeadSHA       = "abcdef1234567890"
)

// newScheme registers the Terraform and GitRepository GVKs as
// unstructured so the fake client can store them.
func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()

	s.AddKnownTypeWithName(floor.TerraformGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(floor.TerraformGVK.GroupVersion().WithKind(floor.TerraformGVK.Kind+"List"), &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(floor.GitRepositoryGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(floor.GitRepositoryGVK.GroupVersion().WithKind(floor.GitRepositoryGVK.Kind+"List"), &unstructured.UnstructuredList{})

	return s
}

func newGitRepository(branch string, annotations map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": floor.GitRepositoryGVK.GroupVersion().String(),
			"kind":       floor.GitRepositoryGVK.Kind,
			"metadata": map[string]any{
				"name":      testGitRepository,
				"namespace": testNamespace,
			},
			"spec": map[string]any{
				"ref": map[string]any{
					"branch": branch,
				},
			},
		},
	}

	if annotations != nil {
		obj.SetAnnotations(annotations)
	}

	return obj
}

func newTerraform(name string, pending string, labels map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": floor.TerraformGVK.GroupVersion().String(),
			"kind":       floor.TerraformGVK.Kind,
			"metadata": map[string]any{
				"name":      name,
				"namespace": testNamespace,
			},
			"spec": map[string]any{
				"path": "./regional",
				"sourceRef": map[string]any{
					"name": testGitRepository,
				},
			},
			"status": map[string]any{
				"plan": map[string]any{
					"pending": pending,
				},
			},
		},
	}

	if labels != nil {
		obj.SetLabels(labels)
	}

	return obj
}

func plannerLabels() map[string]string {
	return map[string]string{
		"infra.weave.works/pr-id":            "7",
		"infra.weave.works/primary-resource": testPrimary,
	}
}

// newGitHubServer serves the PR fixture and records comment bodies.
func newGitHubServer(t *testing.T, state string, merged bool, comments *[]string) *httptest.Server {
	t.Helper()

	mergedJSON := "false"
	if merged {
		mergedJSON = "true"
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/7"):
			_, _ = io.WriteString(w, `{"state":"`+state+`","merged":`+mergedJSON+`,"head":{"sha":"`+testHeadSHA+`","ref":"`+testHeadRef+`"}}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/comments"):
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			body, _ := io.ReadAll(r.Body)

			*comments = append(*comments, string(body))

			_, _ = io.WriteString(w, `{"id":1}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))

	t.Cleanup(server.Close)

	return server
}

// fakePlanner mimics the Branch Planner: once the GitRepository points at
// branch, it writes pending onto the primary Terraform object.
func fakePlanner(ctx context.Context, t *testing.T, k client.Client, branch, pending string) {
	t.Helper()

	go func() {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			repo := &unstructured.Unstructured{}
			repo.SetGroupVersionKind(floor.GitRepositoryGVK)

			if err := k.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: testGitRepository}, repo); err != nil {
				continue
			}

			current, _, _ := unstructured.NestedString(repo.Object, "spec", "ref", "branch")
			if current != branch {
				continue
			}

			primary := &unstructured.Unstructured{}
			primary.SetGroupVersionKind(floor.TerraformGVK)

			if err := k.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: testPrimary}, primary); err != nil {
				continue
			}

			if err := unstructured.SetNestedField(primary.Object, pending, "status", "plan", "pending"); err != nil {
				return
			}

			if err := k.Update(ctx, primary); err == nil {
				return
			}
		}
	}()
}

func newManager(t *testing.T, k client.Client, server *httptest.Server) *Manager {
	t.Helper()

	gh, err := githubapi.NewClient("token", "o", "r", server.URL)
	if err != nil {
		t.Fatalf("githubapi.NewClient: %v", err)
	}

	m := NewManager(floor.NewClient(k, testNamespace), gh, k, testNamespace, testGitRepository)
	m.PollInterval = time.Millisecond
	m.RevisionTimeout = 5 * time.Second

	return m
}

func gitRepositoryState(t *testing.T, k client.Client) (branch string, annotations map[string]string) {
	t.Helper()

	repo := &unstructured.Unstructured{}
	repo.SetGroupVersionKind(floor.GitRepositoryGVK)

	if err := k.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: testGitRepository}, repo); err != nil {
		t.Fatalf("getting GitRepository: %v", err)
	}

	branch, _, _ = unstructured.NestedString(repo.Object, "spec", "ref", "branch")

	return branch, repo.GetAnnotations()
}

func approvePlan(t *testing.T, k client.Client) string {
	t.Helper()

	primary := &unstructured.Unstructured{}
	primary.SetGroupVersionKind(floor.TerraformGVK)

	if err := k.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: testPrimary}, primary); err != nil {
		t.Fatalf("getting primary: %v", err)
	}

	value, _, _ := unstructured.NestedString(primary.Object, "spec", "approvePlan")

	return value
}

func TestApproveRepointsThenApproves(t *testing.T) {
	k := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		newGitRepository("main", nil),
		newTerraform(testPrimary, "", nil),
		newTerraform(testPlanner, testPlanID, plannerLabels()),
	).Build()

	var comments []string

	server := newGitHubServer(t, "open", false, &comments)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fakePlanner(ctx, t, k, testHeadRef, testPlanID)

	if _, err := newManager(t, k, server).ApprovePullRequestPlan(ctx, testPlanner, testPlanID, false, "brettcurtis"); err != nil {
		t.Fatalf("ApprovePullRequestPlan: %v", err)
	}

	branch, annotations := gitRepositoryState(t, k)
	if branch != testHeadRef {
		t.Errorf("branch = %q, want %q", branch, testHeadRef)
	}

	if annotations["floor.osinfra.io/pull-request"] != "7" {
		t.Errorf("annotation = %q, want %q", annotations["floor.osinfra.io/pull-request"], "7")
	}

	if got := approvePlan(t, k); got != testPlanID {
		t.Errorf("approvePlan = %q, want %q", got, testPlanID)
	}

	if len(comments) != 1 || !strings.Contains(comments[0], "brettcurtis") {
		t.Errorf("unexpected comments %v", comments)
	}
}

func TestApproveStopsOnRevisionMismatch(t *testing.T) {
	k := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		newGitRepository("main", nil),
		newTerraform(testPrimary, "", nil),
		newTerraform(testPlanner, testPlanID, plannerLabels()),
	).Build()

	var comments []string

	server := newGitHubServer(t, "open", false, &comments)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fakePlanner(ctx, t, k, testHeadRef, "plan-floor-test-7-9999999999")

	_, err := newManager(t, k, server).ApprovePullRequestPlan(ctx, testPlanner, testPlanID, false, "brettcurtis")

	var mismatch ErrRevisionMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v, want ErrRevisionMismatch", err)
	}

	if mismatch.Expected != "abcdef1234" || mismatch.Observed != "plan-floor-test-7-9999999999" {
		t.Errorf("unexpected mismatch %+v", mismatch)
	}

	if got := approvePlan(t, k); got != "" {
		t.Errorf("approvePlan = %q, want empty", got)
	}

	if branch, _ := gitRepositoryState(t, k); branch != testHeadRef {
		t.Errorf("branch = %q, want %q", branch, testHeadRef)
	}

	if len(comments) != 0 {
		t.Errorf("unexpected comments %v", comments)
	}
}

func TestApproveIgnoresStalePendingPlan(t *testing.T) {
	const stalePlan = "plan-main-0123456789"

	k := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		newGitRepository("main", nil),
		newTerraform(testPrimary, stalePlan, nil),
		newTerraform(testPlanner, testPlanID, plannerLabels()),
	).Build()

	var comments []string

	server := newGitHubServer(t, "open", false, &comments)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fakePlanner(ctx, t, k, testHeadRef, testPlanID)

	plan, err := newManager(t, k, server).ApprovePullRequestPlan(ctx, testPlanner, testPlanID, false, "brettcurtis")
	if err != nil {
		t.Fatalf("ApprovePullRequestPlan: %v", err)
	}

	if plan != testPlanID {
		t.Errorf("plan = %q, want %q", plan, testPlanID)
	}

	if got := approvePlan(t, k); got != testPlanID {
		t.Errorf("approvePlan = %q, want %q", got, testPlanID)
	}
}

func TestApproveStopsOnRevisionMismatchAfterStalePendingPlan(t *testing.T) {
	const (
		stalePlan     = "plan-main-0123456789"
		unrelatedPlan = "plan-floor-test-7-9999999999"
	)

	k := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		newGitRepository("main", nil),
		newTerraform(testPrimary, stalePlan, nil),
		newTerraform(testPlanner, testPlanID, plannerLabels()),
	).Build()

	var comments []string

	server := newGitHubServer(t, "open", false, &comments)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fakePlanner(ctx, t, k, testHeadRef, unrelatedPlan)

	_, err := newManager(t, k, server).ApprovePullRequestPlan(ctx, testPlanner, testPlanID, false, "brettcurtis")

	var mismatch ErrRevisionMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v, want ErrRevisionMismatch", err)
	}

	if mismatch.Observed != unrelatedPlan {
		t.Errorf("observed = %q, want %q", mismatch.Observed, unrelatedPlan)
	}

	if got := approvePlan(t, k); got != "" {
		t.Errorf("approvePlan = %q, want empty", got)
	}
}

func TestSecondApprovalOverwrites(t *testing.T) {
	k := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		newGitRepository("floor-test-3", map[string]string{"floor.osinfra.io/pull-request": "3"}),
		newTerraform(testPrimary, "", nil),
		newTerraform(testPlanner, testPlanID, plannerLabels()),
	).Build()

	var comments []string

	server := newGitHubServer(t, "open", false, &comments)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fakePlanner(ctx, t, k, testHeadRef, testPlanID)

	if _, err := newManager(t, k, server).ApprovePullRequestPlan(ctx, testPlanner, testPlanID, false, "brettcurtis"); err != nil {
		t.Fatalf("ApprovePullRequestPlan: %v", err)
	}

	branch, annotations := gitRepositoryState(t, k)
	if branch != testHeadRef {
		t.Errorf("branch = %q, want %q", branch, testHeadRef)
	}

	if annotations["floor.osinfra.io/pull-request"] != "7" {
		t.Errorf("annotation = %q, want %q", annotations["floor.osinfra.io/pull-request"], "7")
	}
}

func TestReturnToMainWhenMerged(t *testing.T) {
	k := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		newGitRepository(testHeadRef, map[string]string{"floor.osinfra.io/pull-request": "7"}),
	).Build()

	var comments []string

	server := newGitHubServer(t, "closed", true, &comments)

	returned, err := newManager(t, k, server).ReturnToMainIfClosed(context.Background())
	if err != nil {
		t.Fatalf("ReturnToMainIfClosed: %v", err)
	}

	if !returned {
		t.Fatal("returned = false, want true")
	}

	branch, annotations := gitRepositoryState(t, k)
	if branch != "main" {
		t.Errorf("branch = %q, want main", branch)
	}

	if _, ok := annotations["floor.osinfra.io/pull-request"]; ok {
		t.Errorf("annotation still present: %v", annotations)
	}
}

func TestReturnToMainNoopWhenOpen(t *testing.T) {
	k := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		newGitRepository(testHeadRef, map[string]string{"floor.osinfra.io/pull-request": "7"}),
	).Build()

	var comments []string

	server := newGitHubServer(t, "open", false, &comments)

	returned, err := newManager(t, k, server).ReturnToMainIfClosed(context.Background())
	if err != nil {
		t.Fatalf("ReturnToMainIfClosed: %v", err)
	}

	if returned {
		t.Fatal("returned = true, want false")
	}

	if branch, _ := gitRepositoryState(t, k); branch != testHeadRef {
		t.Errorf("branch = %q, want %q", branch, testHeadRef)
	}
}

func TestApproveRejectsPlanForAnOlderRevision(t *testing.T) {
	const olderPlan = "plan-floor-test-7-1111111111"

	k := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		newGitRepository("main", nil),
		newTerraform(testPrimary, "", nil),
		newTerraform(testPlanner, olderPlan, plannerLabels()),
	).Build()

	var comments []string

	server := newGitHubServer(t, "open", false, &comments)

	_, err := newManager(t, k, server).ApprovePullRequestPlan(context.Background(), testPlanner, olderPlan, false, "brettcurtis")

	var mismatch ErrRevisionMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v, want ErrRevisionMismatch", err)
	}

	if branch, annotations := gitRepositoryState(t, k); branch != "main" || len(annotations) != 0 {
		t.Errorf("GitRepository was changed: branch %q, annotations %v", branch, annotations)
	}

	if got := approvePlan(t, k); got != "" {
		t.Errorf("approvePlan = %q, want empty", got)
	}
}

func TestApproveSucceedsWhenCommentFails(t *testing.T) {
	k := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		newGitRepository("main", nil),
		newTerraform(testPrimary, "", nil),
		newTerraform(testPlanner, testPlanID, plannerLabels()),
	).Build()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pulls/7") {
			_, _ = io.WriteString(w, `{"state":"open","merged":false,"head":{"sha":"`+testHeadSHA+`","ref":"`+testHeadRef+`"}}`)

			return
		}

		w.WriteHeader(http.StatusInternalServerError)
	}))

	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fakePlanner(ctx, t, k, testHeadRef, testPlanID)

	plan, err := newManager(t, k, server).ApprovePullRequestPlan(ctx, testPlanner, testPlanID, false, "brettcurtis")
	if err != nil {
		t.Fatalf("ApprovePullRequestPlan: %v", err)
	}

	if plan != testPlanID {
		t.Errorf("plan = %q, want %q", plan, testPlanID)
	}

	if got := approvePlan(t, k); got != testPlanID {
		t.Errorf("approvePlan = %q, want %q", got, testPlanID)
	}
}

func TestApproveAcceptsPlanAlreadyPendingOnRetry(t *testing.T) {
	k := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		newGitRepository(testHeadRef, map[string]string{"floor.osinfra.io/pull-request": "7"}),
		newTerraform(testPrimary, testPlanID, nil),
		newTerraform(testPlanner, testPlanID, plannerLabels()),
	).Build()

	var comments []string

	server := newGitHubServer(t, "open", false, &comments)

	m := newManager(t, k, server)
	m.RevisionTimeout = time.Second

	plan, err := m.ApprovePullRequestPlan(context.Background(), testPlanner, testPlanID, false, "brettcurtis")
	if err != nil {
		t.Fatalf("ApprovePullRequestPlan: %v", err)
	}

	if plan != testPlanID {
		t.Errorf("plan = %q, want %q", plan, testPlanID)
	}

	if got := approvePlan(t, k); got != testPlanID {
		t.Errorf("approvePlan = %q, want %q", got, testPlanID)
	}
}

func TestReturnToMainKeepsNewerSelection(t *testing.T) {
	const newerBranch = "floor-test-9"

	var repointed bool

	k := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		newGitRepository(testHeadRef, map[string]string{"floor.osinfra.io/pull-request": "7"}),
	).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if err := c.Get(ctx, key, obj, opts...); err != nil {
				return err
			}

			if repointed {
				return nil
			}

			repointed = true

			// A concurrent approval repoints the GitRepository at pull
			// request 9 after the close check read it.
			current := obj.DeepCopyObject().(*unstructured.Unstructured)

			if err := unstructured.SetNestedField(current.Object, newerBranch, "spec", "ref", "branch"); err != nil {
				return err
			}

			current.SetAnnotations(map[string]string{"floor.osinfra.io/pull-request": "9"})

			return c.Update(ctx, current)
		},
	}).Build()

	var comments []string

	server := newGitHubServer(t, "closed", true, &comments)

	returned, err := newManager(t, k, server).ReturnToMainIfClosed(context.Background())
	if err != nil {
		t.Fatalf("ReturnToMainIfClosed: %v", err)
	}

	if returned {
		t.Error("returned = true, want false on a stale close check")
	}

	branch, annotations := gitRepositoryState(t, k)
	if branch != newerBranch {
		t.Errorf("branch = %q, want %q", branch, newerBranch)
	}

	if annotations["floor.osinfra.io/pull-request"] != "9" {
		t.Errorf("annotation = %q, want 9", annotations["floor.osinfra.io/pull-request"])
	}
}
