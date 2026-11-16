package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/osinfra-io/pt-techne-floor/internal/floor"
	"github.com/osinfra-io/pt-techne-floor/internal/githubapi"
	"github.com/osinfra-io/pt-techne-floor/internal/sandbox"
)

const (
	testNamespace     = "pt-techne-floor-fixture"
	testGitRepository = "fixture"
	testApproverGroup = "pt-techne: floor Approvers"
	testPlanID        = "plan-main-49203ac458"
)

// newScheme registers the Terraform and GitRepository GVKs as unstructured
// so the fake client can store them.
func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()

	s.AddKnownTypeWithName(floor.TerraformGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(floor.TerraformGVK.GroupVersion().WithKind(floor.TerraformGVK.Kind+"List"), &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(floor.GitRepositoryGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(floor.GitRepositoryGVK.GroupVersion().WithKind(floor.GitRepositoryGVK.Kind+"List"), &unstructured.UnstructuredList{})

	return s
}

func newTerraform(name, path, pending string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": floor.TerraformGVK.GroupVersion().String(),
			"kind":       floor.TerraformGVK.Kind,
			"metadata": map[string]any{
				"name":      name,
				"namespace": testNamespace,
			},
			"spec": map[string]any{
				"path": path,
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
}

func newGitRepository() *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": floor.GitRepositoryGVK.GroupVersion().String(),
			"kind":       floor.GitRepositoryGVK.Kind,
			"metadata": map[string]any{
				"name":      testGitRepository,
				"namespace": testNamespace,
			},
			"spec": map[string]any{
				"ref": map[string]any{
					"branch": "main",
				},
			},
		},
	}
}

// newGitHubServer stands in for github.com; these tests never reach it, but
// the sandbox manager needs a client that is not pointed at github.com.
func newGitHubServer(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected GitHub request %s %s", r.Method, r.URL.Path)
		_, _ = io.WriteString(w, `{}`)
	}))

	t.Cleanup(server.Close)

	return server
}

type harness struct {
	t        *testing.T
	k        client.Client
	session  *mcp.ClientSession
	recorder *record.FakeRecorder
}

// newHarness wires the MCP server over a fake cluster and connects an
// in-memory client session whose context carries identity.
func newHarness(t *testing.T, identity Identity, additionalNamespaces ...string) *harness {
	t.Helper()

	k := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(
			newTerraform("main-sandbox", "./main", testPlanID),
			newTerraform("regional-sandbox", "./regional", ""),
			newGitRepository(),
		).
		Build()

	fl := floor.NewClient(k, testNamespace, additionalNamespaces...)

	gh, err := githubapi.NewClient("token", "osinfra-io", "pt-techne-floor-fixture", newGitHubServer(t).URL)
	if err != nil {
		t.Fatalf("githubapi.NewClient: %v", err)
	}

	sb := sandbox.NewManager(floor.NewClient(k, testNamespace), gh, k, testNamespace, testGitRepository)
	recorder := record.NewFakeRecorder(10)

	server := New(fl, sb, recorder, testApproverGroup)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	ctx, cancel := context.WithTimeout(WithIdentity(context.Background(), identity), 30*time.Second)
	t.Cleanup(cancel)

	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server.Connect: %v", err)
	}

	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}

	t.Cleanup(func() { _ = session.Close() })

	return &harness{t: t, k: k, session: session, recorder: recorder}
}

func (h *harness) call(name string, arguments any) *mcp.CallToolResult {
	h.t.Helper()

	result, err := h.session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		h.t.Fatalf("CallTool %s: %v", name, err)
	}

	return result
}

// approvePlan reads spec.approvePlan straight from the fake cluster.
func (h *harness) approvePlan(name string) string {
	h.t.Helper()

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(floor.TerraformGVK)

	if err := h.k.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: name}, obj); err != nil {
		h.t.Fatalf("getting %s: %v", name, err)
	}

	value, _, _ := unstructured.NestedString(obj.Object, "spec", "approvePlan")

	return value
}

func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()

	var builder strings.Builder

	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if !ok {
			continue
		}

		builder.WriteString(text.Text)
	}

	return builder.String()
}

func TestListWorkspaces(t *testing.T) {
	h := newHarness(t, Identity{User: "brett", Groups: []string{testApproverGroup}})

	result := h.call("list_workspaces", map[string]any{})
	if result.IsError {
		t.Fatalf("list_workspaces returned an error: %s", resultText(t, result))
	}

	text := resultText(t, result)

	for _, name := range []string{"main-sandbox", "regional-sandbox"} {
		if !strings.Contains(text, name) {
			t.Errorf("list_workspaces output does not contain %q: %s", name, text)
		}
	}
}

func TestBootstrapWorkspaceThroughFloor(t *testing.T) {
	const namespace = "pt-bootstrap-sandbox"
	const name = namespace + "/bootstrap-sandbox"

	for _, tc := range []struct {
		name     string
		groups   []string
		planID   string
		approved bool
	}{
		{name: "approver", groups: []string{testApproverGroup}, planID: testPlanID, approved: true},
		{name: "reader", planID: testPlanID},
		{name: "stale plan", groups: []string{testApproverGroup}, planID: "plan-main-aaaaaaaaaa"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Identity{User: "operator", Groups: tc.groups}, namespace)
			seed := newTerraform("bootstrap-sandbox", "./bootstrap/sandbox", testPlanID)
			seed.SetNamespace(namespace)
			if err := h.k.Create(context.Background(), seed); err != nil {
				t.Fatal(err)
			}

			for _, tool := range []string{"list_workspaces", "pending_approvals", "get_workspace"} {
				arguments := map[string]any{}
				if tool == "get_workspace" {
					arguments["name"] = name
				}
				result := h.call(tool, arguments)
				if result.IsError || !strings.Contains(resultText(t, result), name) {
					t.Fatalf("%s did not expose the bootstrap workspace: %s", tool, resultText(t, result))
				}
			}

			result := h.call("approve", map[string]any{"name": name, "planId": tc.planID})
			if result.IsError == tc.approved {
				t.Fatalf("approve error = %t, want %t: %s", result.IsError, !tc.approved, resultText(t, result))
			}
			if err := h.k.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: "bootstrap-sandbox"}, seed); err != nil {
				t.Fatal(err)
			}
			approval, _, _ := unstructured.NestedString(seed.Object, "spec", "approvePlan")
			if (approval == testPlanID) != tc.approved {
				t.Fatalf("bootstrap approval = %q, approved = %t", approval, tc.approved)
			}
			if h.approvePlan("main-sandbox") != "" {
				t.Fatal("bootstrap approval changed the fixture")
			}
		})
	}
}

func TestAdditionalNamespaceCannotRepointFixture(t *testing.T) {
	const namespace = "pt-bootstrap-sandbox"
	h := newHarness(t, Identity{User: "operator", Groups: []string{testApproverGroup}}, namespace)
	planner := newTerraform("bootstrap-pr", "./bootstrap/sandbox", testPlanID)
	planner.SetNamespace(namespace)
	planner.SetLabels(map[string]string{
		"infra.weave.works/pr-id":            "42",
		"infra.weave.works/primary-resource": "main-sandbox",
	})
	if err := h.k.Create(context.Background(), planner); err != nil {
		t.Fatal(err)
	}

	result := h.call("approve", map[string]any{"name": namespace + "/bootstrap-pr", "planId": testPlanID})
	if !result.IsError || !strings.Contains(resultText(t, result), "not configured") {
		t.Fatalf("approve did not reject cross-namespace PR repointing: %s", resultText(t, result))
	}
	if h.approvePlan("main-sandbox") != "" {
		t.Fatal("cross-namespace planner approved the fixture")
	}
}

func TestApproveRecordsEvent(t *testing.T) {
	h := newHarness(t, Identity{User: "brett", Groups: []string{testApproverGroup}})

	result := h.call("approve", map[string]any{"name": "main-sandbox", "planId": testPlanID})
	if result.IsError {
		t.Fatalf("approve returned an error: %s", resultText(t, result))
	}

	if got := h.approvePlan("main-sandbox"); got != testPlanID {
		t.Errorf("spec.approvePlan is %q, want %q", got, testPlanID)
	}

	select {
	case event := <-h.recorder.Events:
		if !strings.Contains(event, "Approved") || !strings.Contains(event, testPlanID) || !strings.Contains(event, "brett") {
			t.Errorf("event %q does not record the approver and plan id", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event was recorded for the approval")
	}
}

func TestApproveForbiddenWithoutGroup(t *testing.T) {
	h := newHarness(t, Identity{User: "brett", Groups: []string{"pt-pneuma: kagent Admins"}})

	result := h.call("approve", map[string]any{"name": "main-sandbox", "planId": testPlanID})
	if !result.IsError {
		t.Fatal("approve succeeded without the approver group")
	}

	if text := resultText(t, result); !strings.Contains(text, "forbidden") {
		t.Errorf("error %q does not contain \"forbidden\"", text)
	}

	if got := h.approvePlan("main-sandbox"); got != "" {
		t.Errorf("spec.approvePlan is %q, want the object left unpatched", got)
	}

	select {
	case event := <-h.recorder.Events:
		t.Errorf("a forbidden approval recorded event %q", event)
	default:
	}
}

func TestApproveRejectsPlanMismatch(t *testing.T) {
	h := newHarness(t, Identity{User: "brett", Groups: []string{testApproverGroup}})

	result := h.call("approve", map[string]any{"name": "main-sandbox", "planId": "plan-main-0000000000"})
	if !result.IsError {
		t.Fatal("approve accepted a stale plan id")
	}

	text := resultText(t, result)

	if !strings.Contains(text, "mismatch") {
		t.Errorf("error %q does not report a mismatch", text)
	}

	if !strings.Contains(text, "call replan on main-sandbox and approve the new plan id") {
		t.Errorf("error %q does not include replan guidance", text)
	}
}

func TestPendingApprovalsOnlyListsPendingPlans(t *testing.T) {
	h := newHarness(t, Identity{User: "brett", Groups: []string{testApproverGroup}})

	text := resultText(t, h.call("pending_approvals", map[string]any{}))

	if !strings.Contains(text, "main-sandbox") {
		t.Errorf("pending_approvals omits main-sandbox: %s", text)
	}

	if strings.Contains(text, "regional-sandbox") {
		t.Errorf("pending_approvals lists regional-sandbox, which has no pending plan: %s", text)
	}
}

func TestReadsRequireAnIdentity(t *testing.T) {
	h := newHarness(t, Identity{})

	result := h.call("list_workspaces", map[string]any{})
	if !result.IsError {
		t.Fatal("list_workspaces succeeded without an identity")
	}

	if text := resultText(t, result); !strings.Contains(text, "unauthenticated") {
		t.Errorf("error %q does not report an unauthenticated caller", text)
	}
}

func TestIdentityParsesGroups(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.Header.Set("X-authentik-username", "brett")
	request.Header.Set("X-authentik-groups", "a| b")

	identity := IdentityFromRequest(request)

	if identity.User != "brett" {
		t.Errorf("user is %q, want brett", identity.User)
	}

	if len(identity.Groups) != 2 || identity.Groups[0] != "a" || identity.Groups[1] != "b" {
		t.Errorf("groups are %#v, want [a b]", identity.Groups)
	}
}

func TestHandlerCarriesIdentityIntoTheContext(t *testing.T) {
	var observed Identity

	handler := withIdentity(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		observed = IdentityFromContext(r.Context())
	}))

	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.Header.Set("X-authentik-username", "brett")
	request.Header.Set("X-authentik-groups", testApproverGroup)

	handler.ServeHTTP(httptest.NewRecorder(), request)

	if observed.User != "brett" || !observed.InGroup(testApproverGroup) {
		t.Errorf("handler stored %#v in the request context", observed)
	}
}
