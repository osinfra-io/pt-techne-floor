package floor

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Errors returned by Approve and ValidateApproval.
var (
	ErrNoPendingPlan          = errors.New("no pending plan")
	ErrPlanMismatch           = errors.New("plan id mismatch")
	ErrDestroysUnacknowledged = errors.New("plan contains destroys and was not acknowledged")
)

// Client is a typed wrapper over a controller-runtime client scoped to
// tofu-controller Terraform objects in a single namespace.
type Client struct {
	k         client.Client
	namespace string
}

// NewClient returns a Client that reads and patches Terraform objects in
// namespace using k.
func NewClient(k client.Client, namespace string) *Client {
	return &Client{k: k, namespace: namespace}
}

// List returns every Terraform object in the client's namespace, mapped to
// Workspace.
func (c *Client) List(ctx context.Context) ([]Workspace, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(TerraformGVK.GroupVersion().WithKind(TerraformGVK.Kind + "List"))

	if err := c.k.List(ctx, list, client.InNamespace(c.namespace)); err != nil {
		return nil, fmt.Errorf("listing Terraform objects: %w", err)
	}

	workspaces := make([]Workspace, 0, len(list.Items))

	for i := range list.Items {
		ws, err := c.toWorkspace(ctx, &list.Items[i])
		if err != nil {
			return nil, err
		}

		workspaces = append(workspaces, ws)
	}

	return workspaces, nil
}

// Get returns the Workspace for the named Terraform object.
func (c *Client) Get(ctx context.Context, name string) (Workspace, error) {
	obj, err := c.Workspace(ctx, name)
	if err != nil {
		return Workspace{}, err
	}

	return c.toWorkspace(ctx, obj)
}

// Workspace returns the raw unstructured Terraform object for name.
func (c *Client) Workspace(ctx context.Context, name string) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(TerraformGVK)

	if err := c.k.Get(ctx, types.NamespacedName{Namespace: c.namespace, Name: name}, obj); err != nil {
		return nil, fmt.Errorf("getting Terraform object %q: %w", name, err)
	}

	return obj, nil
}

// ValidateApproval checks whether planID may be approved against ws,
// returning ErrNoPendingPlan, ErrPlanMismatch, or ErrDestroysUnacknowledged
// when it may not.
func ValidateApproval(ws Workspace, planID string, acknowledgeDestroys bool) error {
	if ws.PendingPlan == "" {
		return ErrNoPendingPlan
	}

	if planID != ws.PendingPlan {
		return fmt.Errorf("%w: pending %q, given %q", ErrPlanMismatch, ws.PendingPlan, planID)
	}

	if ws.Review == "destroys" && !acknowledgeDestroys {
		return ErrDestroysUnacknowledged
	}

	return nil
}

// Approve validates planID against the named Terraform object's pending
// plan and, if valid, approves it by setting spec.approvePlan.
func (c *Client) Approve(ctx context.Context, name, planID string, acknowledgeDestroys bool) error {
	ws, err := c.Get(ctx, name)
	if err != nil {
		return err
	}

	if err := ValidateApproval(ws, planID, acknowledgeDestroys); err != nil {
		return err
	}

	return c.SetApprovePlan(ctx, name, planID)
}

// Hold disables reconciliation by setting spec.approvePlan to "disable".
func (c *Client) Hold(ctx context.Context, name string) error {
	return c.SetApprovePlan(ctx, name, "disable")
}

// Unhold clears spec.approvePlan.
func (c *Client) Unhold(ctx context.Context, name string) error {
	return c.SetApprovePlan(ctx, name, "")
}

// Replan requests a fresh plan by setting spec.approvePlan to "replan".
func (c *Client) Replan(ctx context.Context, name string) error {
	return c.SetApprovePlan(ctx, name, "replan")
}

// SetApprovePlan patches spec.approvePlan on the named Terraform object,
// leaving every other field, including spec.destroy, untouched.
func (c *Client) SetApprovePlan(ctx context.Context, name, value string) error {
	return c.mergePatch(ctx, name, fmt.Sprintf(`{"spec":{"approvePlan":%q}}`, value))
}

// SetDestroy patches spec.destroy: true on the named Terraform object and
// clears spec.approvePlan in the same patch, so an approval granted for an
// earlier plan cannot apply the destruction.
func (c *Client) SetDestroy(ctx context.Context, name string) error {
	return c.mergePatch(ctx, name, `{"spec":{"approvePlan":null,"destroy":true}}`)
}

// mergePatch applies a field-targeted JSON merge patch to the named
// Terraform object.
func (c *Client) mergePatch(ctx context.Context, name, patch string) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(TerraformGVK)
	obj.SetName(name)
	obj.SetNamespace(c.namespace)

	if err := c.k.Patch(ctx, obj, client.RawPatch(types.MergePatchType, []byte(patch)), client.FieldOwner("floor-api")); err != nil {
		return fmt.Errorf("patching Terraform object %q with %s: %w", name, patch, err)
	}

	return nil
}

// toWorkspace flattens a Terraform object, and, when its sourceRef
// GitRepository can be found, its source branch, into a Workspace.
func (c *Client) toWorkspace(ctx context.Context, obj *unstructured.Unstructured) (Workspace, error) {
	ws := Workspace{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}

	ws.Path, _, _ = unstructured.NestedString(obj.Object, "spec", "path")
	ws.Destroy, _, _ = unstructured.NestedBool(obj.Object, "spec", "destroy")
	ws.PlanOnly, _, _ = unstructured.NestedBool(obj.Object, "spec", "planOnly")

	ws.PendingPlan, _, _ = unstructured.NestedString(obj.Object, "status", "plan", "pending")
	ws.LastAppliedRevision, _, _ = unstructured.NestedString(obj.Object, "status", "lastAppliedRevision")

	ws.Phase = readyReason(obj)

	labels := obj.GetLabels()
	if pr, err := strconv.Atoi(labels[labelPullRequestID]); err == nil {
		ws.PullRequest = pr
	}

	ws.PrimaryResource = labels[labelPrimaryResource]

	ws.Review = obj.GetAnnotations()[annotationReview]

	sourceName, _, _ := unstructured.NestedString(obj.Object, "spec", "sourceRef", "name")
	if sourceName != "" {
		branch, err := c.sourceBranch(ctx, sourceName, obj.GetNamespace())
		if err != nil {
			return Workspace{}, err
		}

		ws.SourceBranch = branch
	}

	return ws, nil
}

// sourceBranch reads spec.ref.branch from the named GitRepository,
// returning an empty string rather than an error when it is missing.
func (c *Client) sourceBranch(ctx context.Context, name, namespace string) (string, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(GitRepositoryGVK)

	err := c.k.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, obj)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			return "", nil
		}

		return "", fmt.Errorf("getting GitRepository %q: %w", name, err)
	}

	branch, _, _ := unstructured.NestedString(obj.Object, "spec", "ref", "branch")

	return branch, nil
}

// readyReason returns the reason of the status.conditions entry with
// type == "Ready", or "" if absent.
func readyReason(obj *unstructured.Unstructured) string {
	conditions, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil || !found {
		return ""
	}

	for _, c := range conditions {
		condition, ok := c.(map[string]any)
		if !ok {
			continue
		}

		if condition["type"] != "Ready" {
			continue
		}

		reason, _ := condition["reason"].(string)

		return reason
	}

	return ""
}
