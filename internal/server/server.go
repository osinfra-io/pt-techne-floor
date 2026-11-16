// Package server exposes the floor Terraform object client and the
// sandbox manager as MCP tools over streamable HTTP, authorizing every
// call against the identity the authentik outpost forwarded.
package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

	"github.com/osinfra-io/pt-techne-floor/internal/floor"
	"github.com/osinfra-io/pt-techne-floor/internal/sandbox"
)

// errUnauthenticated is returned when no identity reached the tool, which
// means the request did not pass through the authentik outpost.
var errUnauthenticated = errors.New("unauthenticated: no authentik identity was forwarded with the request")

// nameInput identifies a single Terraform object.
type nameInput struct {
	Name string `json:"name" jsonschema:"Terraform object name; use namespace/name outside the default namespace"`
}

// approveInput is the argument set of the approve tool.
type approveInput struct {
	Name                string `json:"name" jsonschema:"Terraform object name; use namespace/name outside the default namespace"`
	PlanID              string `json:"planId" jsonschema:"plan id the approver read, which must match the object's pending plan"`
	AcknowledgeDestroys bool   `json:"acknowledgeDestroys,omitempty" jsonschema:"acknowledge that the plan destroys resources"`
}

// rejectInput is the argument set of the reject tool.
type rejectInput struct {
	Name   string `json:"name" jsonschema:"Terraform object name; use namespace/name outside the default namespace"`
	Reason string `json:"reason" jsonschema:"why the pending plan was rejected"`
}

// approveOutput reports the outcome of an approval. Applied is always
// false: tofu-controller applies asynchronously after the approval.
type approveOutput struct {
	Applied bool   `json:"applied"`
	Message string `json:"message"`
}

// messageOutput is the result of the tools that only request a change.
type messageOutput struct {
	Message string `json:"message"`
}

// workspaceOutput is the result of get_workspace.
type workspaceOutput struct {
	Workspace floor.Workspace `json:"workspace"`
}

// workspacesOutput is the result of the listing tools.
type workspacesOutput struct {
	Workspaces []floor.Workspace `json:"workspaces"`
}

// tools holds everything the MCP tool handlers need.
type tools struct {
	approverGroup string
	floor         *floor.Client
	recorder      record.EventRecorder
	sandbox       *sandbox.Manager
}

// New returns an MCP server exposing the floor tools. Mutating tools
// require membership of approverGroup.
func New(fl *floor.Client, sb *sandbox.Manager, recorder record.EventRecorder, approverGroup string) *mcp.Server {
	t := &tools{approverGroup: approverGroup, floor: fl, recorder: recorder, sandbox: sb}
	destructive := true

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "floor-api",
		Title:   "osinfra.io factory floor",
		Version: "local",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_workspaces",
		Description: "List every Terraform workspace on the floor with its phase, pending plan, and source branch.",
		Annotations: &mcp.ToolAnnotations{Title: "List workspaces", ReadOnlyHint: true},
	}, t.listWorkspaces)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_workspace",
		Description: "Read one Terraform workspace on the floor.",
		Annotations: &mcp.ToolAnnotations{Title: "Get workspace", ReadOnlyHint: true},
	}, t.getWorkspace)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "pending_approvals",
		Description: "List the Terraform workspaces whose plans are waiting for an approval.",
		Annotations: &mcp.ToolAnnotations{Title: "Pending approvals", ReadOnlyHint: true},
	}, t.pendingApprovals)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "approve",
		Description: "Approve a workspace's pending plan. Pull request plans are approved on the primary workspace after the sandbox is repointed at the pull request head; that can take up to the revision timeout (180s) while the planner re-plans at the pull request head, and the call is safe to retry.",
		Annotations: &mcp.ToolAnnotations{Title: "Approve a plan"},
	}, t.approve)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "reject",
		Description: "Record that a workspace's pending plan was rejected. The plan is left unapproved.",
		Annotations: &mcp.ToolAnnotations{Title: "Reject a plan"},
	}, t.reject)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "replan",
		Description: "Discard a workspace's pending plan and ask tofu-controller for a fresh one.",
		Annotations: &mcp.ToolAnnotations{Title: "Replan"},
	}, t.replan)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hold",
		Description: "Stop a workspace from reconciling until it is unheld.",
		Annotations: &mcp.ToolAnnotations{Title: "Hold"},
	}, t.hold)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "unhold",
		Description: "Let a held workspace reconcile again.",
		Annotations: &mcp.ToolAnnotations{Title: "Unhold"},
	}, t.unhold)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "destroy",
		Description: "Mark a workspace for destruction. The destroy plan still has to be approved.",
		Annotations: &mcp.ToolAnnotations{Title: "Destroy", DestructiveHint: &destructive},
	}, t.destroy)

	return server
}

// Handler returns the HTTP handler serving the MCP server at /mcp. It is
// stateless so each request is authorized with the identity its own
// headers carry.
func Handler(server *mcp.Server) http.Handler {
	return withIdentity(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	))
}

// reader returns the calling identity, requiring only that the outpost
// authenticated it.
func (t *tools) reader(ctx context.Context) (Identity, error) {
	identity := IdentityFromContext(ctx)
	if identity.User == "" {
		return Identity{}, errUnauthenticated
	}

	return identity, nil
}

// approver returns the calling identity, requiring membership of the
// approver group.
func (t *tools) approver(ctx context.Context) (Identity, error) {
	identity, err := t.reader(ctx)
	if err != nil {
		return Identity{}, err
	}

	if !identity.InGroup(t.approverGroup) {
		return Identity{}, fmt.Errorf("forbidden: %s is not in %s", identity.User, t.approverGroup)
	}

	return identity, nil
}

// event records a Kubernetes Event on the named Terraform object.
func (t *tools) event(ctx context.Context, name, reason, messageFormat string, args ...any) error {
	obj, err := t.floor.Workspace(ctx, name)
	if err != nil {
		return err
	}

	t.recorder.Eventf(obj, corev1.EventTypeNormal, reason, messageFormat, args...)

	return nil
}

func (t *tools) listWorkspaces(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, workspacesOutput, error) {
	if _, err := t.reader(ctx); err != nil {
		return nil, workspacesOutput{}, err
	}

	workspaces, err := t.floor.List(ctx)
	if err != nil {
		return nil, workspacesOutput{}, err
	}

	return nil, workspacesOutput{Workspaces: workspaces}, nil
}

func (t *tools) getWorkspace(ctx context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, workspaceOutput, error) {
	if _, err := t.reader(ctx); err != nil {
		return nil, workspaceOutput{}, err
	}

	ws, err := t.floor.Get(ctx, in.Name)
	if err != nil {
		return nil, workspaceOutput{}, err
	}

	return nil, workspaceOutput{Workspace: ws}, nil
}

func (t *tools) pendingApprovals(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, workspacesOutput, error) {
	if _, err := t.reader(ctx); err != nil {
		return nil, workspacesOutput{}, err
	}

	workspaces, err := t.floor.List(ctx)
	if err != nil {
		return nil, workspacesOutput{}, err
	}

	pending := make([]floor.Workspace, 0, len(workspaces))

	for _, ws := range workspaces {
		if ws.PendingPlan != "" {
			pending = append(pending, ws)
		}
	}

	return nil, workspacesOutput{Workspaces: pending}, nil
}

func (t *tools) approve(ctx context.Context, _ *mcp.CallToolRequest, in approveInput) (*mcp.CallToolResult, approveOutput, error) {
	identity, err := t.approver(ctx)
	if err != nil {
		return nil, approveOutput{}, err
	}

	ws, err := t.floor.Get(ctx, in.Name)
	if err != nil {
		return nil, approveOutput{}, err
	}

	// A planner object only ever plans; its approval is carried out on the
	// primary workspace once the sandbox points at the pull request head.
	applied := in.Name
	approvedPlan := in.PlanID

	if ws.PullRequest != 0 {
		applied = ws.PrimaryResource

		approvedPlan, err = t.sandbox.ApprovePullRequestPlan(ctx, in.Name, in.PlanID, in.AcknowledgeDestroys, identity.User)
	} else {
		err = t.floor.Approve(ctx, in.Name, in.PlanID, in.AcknowledgeDestroys)
	}

	if err != nil {
		return nil, approveOutput{}, approvalError(in.Name, err)
	}

	// The approval is the contract and the apply is already starting; the
	// Event is telemetry, so a failure to record it is logged, not returned.
	if err := t.event(ctx, applied, "Approved", "%s approved %s", identity.User, approvedPlan); err != nil {
		log.Printf("recording Approved event for %q: %v", applied, err)
	}

	return nil, approveOutput{
		Message: fmt.Sprintf("plan %s was approved by %s; tofu-controller applies %s next", approvedPlan, identity.User, applied),
	}, nil
}

func (t *tools) reject(ctx context.Context, _ *mcp.CallToolRequest, in rejectInput) (*mcp.CallToolResult, messageOutput, error) {
	identity, err := t.approver(ctx)
	if err != nil {
		return nil, messageOutput{}, err
	}

	ws, err := t.floor.Get(ctx, in.Name)
	if err != nil {
		return nil, messageOutput{}, err
	}

	if ws.PendingPlan == "" {
		return nil, messageOutput{}, floor.ErrNoPendingPlan
	}

	if err := t.event(ctx, in.Name, "Rejected", "%s rejected %s: %s", identity.User, ws.PendingPlan, in.Reason); err != nil {
		return nil, messageOutput{}, err
	}

	return nil, messageOutput{
		Message: fmt.Sprintf("plan %s was rejected by %s and left unapproved", ws.PendingPlan, identity.User),
	}, nil
}

func (t *tools) replan(ctx context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, messageOutput, error) {
	return t.request(ctx, in.Name, "replanned", t.floor.Replan)
}

func (t *tools) hold(ctx context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, messageOutput, error) {
	return t.request(ctx, in.Name, "held", t.floor.Hold)
}

func (t *tools) unhold(ctx context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, messageOutput, error) {
	return t.request(ctx, in.Name, "unheld", t.floor.Unhold)
}

func (t *tools) destroy(ctx context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, messageOutput, error) {
	return t.request(ctx, in.Name, "marked for destruction", t.floor.SetDestroy)
}

// request authorizes the caller, applies a single-field change, and
// reports it in the same shape for every tool that takes only a name.
func (t *tools) request(ctx context.Context, name, outcome string, change func(context.Context, string) error) (*mcp.CallToolResult, messageOutput, error) {
	identity, err := t.approver(ctx)
	if err != nil {
		return nil, messageOutput{}, err
	}

	if err := change(ctx, name); err != nil {
		return nil, messageOutput{}, err
	}

	return nil, messageOutput{Message: fmt.Sprintf("%s was %s by %s", name, outcome, identity.User)}, nil
}

// approvalError adds the operator's next step to the two failures that a
// fresh plan resolves.
func approvalError(name string, err error) error {
	var revision sandbox.ErrRevisionMismatch

	if errors.Is(err, floor.ErrPlanMismatch) || errors.As(err, &revision) {
		return fmt.Errorf("%w: call replan on %s and approve the new plan id", err, name)
	}

	return err
}
