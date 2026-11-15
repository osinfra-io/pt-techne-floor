package floor

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// newScheme registers the Terraform and GitRepository GVKs (and their list
// kinds) as unstructured so the fake client can store them.
func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()

	s.AddKnownTypeWithName(TerraformGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(TerraformGVK.GroupVersion().WithKind(TerraformGVK.Kind+"List"), &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(GitRepositoryGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(GitRepositoryGVK.GroupVersion().WithKind(GitRepositoryGVK.Kind+"List"), &unstructured.UnstructuredList{})

	return s
}

func newTerraform(name, namespace string, spec map[string]any, status map[string]any, labels, annotations map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": TerraformGVK.GroupVersion().String(),
			"kind":       TerraformGVK.Kind,
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
			},
			"spec": spec,
		},
	}

	if status != nil {
		obj.Object["status"] = status
	}

	if labels != nil {
		obj.SetLabels(labels)
	}

	if annotations != nil {
		obj.SetAnnotations(annotations)
	}

	return obj
}

func readyCondition(reason string) []any {
	return []any{
		map[string]any{
			"type":   "Ready",
			"reason": reason,
		},
	}
}

func TestListMapsStatus(t *testing.T) {
	obj := newTerraform(
		"main-sandbox",
		"pt-techne-floor-fixture",
		map[string]any{
			"path": "./main",
			"sourceRef": map[string]any{
				"name": "fixture",
			},
		},
		map[string]any{
			"plan": map[string]any{
				"pending": "plan-main-49203ac458",
			},
			"lastAppliedRevision": "main@sha1:49203ac458",
			"conditions":          readyCondition("TerraformPlannedWithChanges"),
		},
		map[string]string{
			"infra.weave.works/branch-planner":   "true",
			"infra.weave.works/pr-id":            "42",
			"infra.weave.works/primary-resource": "main-sandbox",
		},
		nil,
	)

	c := NewClient(fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(obj).Build(), "pt-techne-floor-fixture")

	workspaces, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(workspaces) != 1 {
		t.Fatalf("List() returned %d workspaces, want 1", len(workspaces))
	}

	ws := workspaces[0]

	if ws.PendingPlan != "plan-main-49203ac458" {
		t.Errorf("PendingPlan = %q, want %q", ws.PendingPlan, "plan-main-49203ac458")
	}

	if ws.Phase != "TerraformPlannedWithChanges" {
		t.Errorf("Phase = %q, want %q", ws.Phase, "TerraformPlannedWithChanges")
	}

	if ws.PullRequest != 42 {
		t.Errorf("PullRequest = %d, want 42", ws.PullRequest)
	}

	if ws.PrimaryResource != "main-sandbox" {
		t.Errorf("PrimaryResource = %q, want %q", ws.PrimaryResource, "main-sandbox")
	}

	if ws.Path != "./main" {
		t.Errorf("Path = %q, want %q", ws.Path, "./main")
	}
}

func TestApproveSetsApprovePlan(t *testing.T) {
	const name = "main-sandbox"

	obj := newTerraform(name, "pt-techne-floor-fixture", map[string]any{"path": "./main"}, map[string]any{
		"plan": map[string]any{"pending": "plan-main-49203ac458"},
	}, nil, nil)

	fakeClient := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(obj).Build()
	c := NewClient(fakeClient, "pt-techne-floor-fixture")

	if err := c.Approve(context.Background(), name, "plan-main-49203ac458", false); err != nil {
		t.Fatalf("Approve() error = %v", err)
	}

	got, err := c.Workspace(context.Background(), name)
	if err != nil {
		t.Fatalf("Workspace() error = %v", err)
	}

	approvePlan, _, _ := unstructured.NestedString(got.Object, "spec", "approvePlan")
	if approvePlan != "plan-main-49203ac458" {
		t.Errorf("spec.approvePlan = %q, want %q", approvePlan, "plan-main-49203ac458")
	}
}

func TestApproveRejectsStalePlan(t *testing.T) {
	const name = "main-sandbox"

	obj := newTerraform(name, "pt-techne-floor-fixture", map[string]any{"path": "./main"}, map[string]any{
		"plan": map[string]any{"pending": "plan-main-aaaaaaaaaa"},
	}, nil, nil)

	fakeClient := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(obj).Build()
	c := NewClient(fakeClient, "pt-techne-floor-fixture")

	err := c.Approve(context.Background(), name, "plan-main-bbbbbbbbbb", false)
	if !errors.Is(err, ErrPlanMismatch) {
		t.Fatalf("Approve() error = %v, want ErrPlanMismatch", err)
	}

	got, err := c.Workspace(context.Background(), name)
	if err != nil {
		t.Fatalf("Workspace() error = %v", err)
	}

	if _, found, _ := unstructured.NestedString(got.Object, "spec", "approvePlan"); found {
		t.Errorf("spec.approvePlan should be unset after a rejected approval")
	}
}

func TestApproveRequiresDestroyAck(t *testing.T) {
	const name = "main-sandbox"
	const planID = "plan-main-49203ac458"

	obj := newTerraform(name, "pt-techne-floor-fixture", map[string]any{"path": "./main"}, map[string]any{
		"plan": map[string]any{"pending": planID},
	}, nil, map[string]string{
		"floor.osinfra.io/review": "destroys",
	})

	fakeClient := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(obj).Build()
	c := NewClient(fakeClient, "pt-techne-floor-fixture")

	if err := c.Approve(context.Background(), name, planID, false); !errors.Is(err, ErrDestroysUnacknowledged) {
		t.Fatalf("Approve() error = %v, want ErrDestroysUnacknowledged", err)
	}

	if err := c.Approve(context.Background(), name, planID, true); err != nil {
		t.Fatalf("Approve() with acknowledgeDestroys = true error = %v", err)
	}
}

func TestHoldSetsDisable(t *testing.T) {
	const name = "main-sandbox"

	obj := newTerraform(name, "pt-techne-floor-fixture", map[string]any{"path": "./main"}, nil, nil, nil)

	fakeClient := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(obj).Build()
	c := NewClient(fakeClient, "pt-techne-floor-fixture")

	if err := c.Hold(context.Background(), name); err != nil {
		t.Fatalf("Hold() error = %v", err)
	}

	got, err := c.Workspace(context.Background(), name)
	if err != nil {
		t.Fatalf("Workspace() error = %v", err)
	}

	approvePlan, _, _ := unstructured.NestedString(got.Object, "spec", "approvePlan")
	if approvePlan != "disable" {
		t.Errorf("spec.approvePlan = %q, want %q", approvePlan, "disable")
	}
}

func TestApprovePreservesDestroy(t *testing.T) {
	const name = "main-sandbox"
	const planID = "plan-main-49203ac458"

	obj := newTerraform(name, "pt-techne-floor-fixture", map[string]any{
		"path":    "./main",
		"destroy": true,
	}, map[string]any{
		"plan": map[string]any{"pending": planID},
	}, nil, nil)

	c := NewClient(fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(obj).Build(), "pt-techne-floor-fixture")

	if err := c.Approve(context.Background(), name, planID, false); err != nil {
		t.Fatalf("Approve() error = %v", err)
	}

	got, err := c.Workspace(context.Background(), name)
	if err != nil {
		t.Fatalf("Workspace() error = %v", err)
	}

	destroy, found, _ := unstructured.NestedBool(got.Object, "spec", "destroy")
	if !found || !destroy {
		t.Errorf("spec.destroy = %t (found %t), want true", destroy, found)
	}

	approvePlan, _, _ := unstructured.NestedString(got.Object, "spec", "approvePlan")
	if approvePlan != planID {
		t.Errorf("spec.approvePlan = %q, want %q", approvePlan, planID)
	}
}

func TestSetDestroyClearsApproval(t *testing.T) {
	const name = "main-sandbox"

	obj := newTerraform(name, "pt-techne-floor-fixture", map[string]any{
		"path":        "./main",
		"approvePlan": "plan-main-49203ac458",
	}, nil, nil, nil)

	c := NewClient(fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(obj).Build(), "pt-techne-floor-fixture")

	if err := c.SetDestroy(context.Background(), name); err != nil {
		t.Fatalf("SetDestroy() error = %v", err)
	}

	got, err := c.Workspace(context.Background(), name)
	if err != nil {
		t.Fatalf("Workspace() error = %v", err)
	}

	if destroy, _, _ := unstructured.NestedBool(got.Object, "spec", "destroy"); !destroy {
		t.Errorf("spec.destroy = false, want true")
	}

	if _, found, _ := unstructured.NestedString(got.Object, "spec", "approvePlan"); found {
		t.Errorf("spec.approvePlan should be cleared when destruction is requested")
	}
}

func TestApproveRejectsPlanNotReflectingSpec(t *testing.T) {
	const name = "main-sandbox"

	obj := newTerraform(name, "pt-techne-floor-fixture", map[string]any{"path": "./main", "destroy": true}, map[string]any{
		"plan":                  map[string]any{"pending": "plan-main-49203ac458"},
		"observedGeneration":    int64(2),
		"lastPlannedGeneration": int64(1),
	}, nil, nil)
	obj.SetGeneration(2)

	fakeClient := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(obj).Build()
	c := NewClient(fakeClient, "pt-techne-floor-fixture")

	err := c.Approve(context.Background(), name, "plan-main-49203ac458", false)
	if !errors.Is(err, ErrPlanStale) {
		t.Fatalf("Approve() error = %v, want ErrPlanStale", err)
	}
}
