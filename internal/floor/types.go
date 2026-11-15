// Package floor provides a typed client over tofu-controller Terraform
// objects, reading their status and approving pending plans via
// server-side apply.
package floor

import "k8s.io/apimachinery/pkg/runtime/schema"

// TerraformGVK identifies the tofu-controller Terraform custom resource.
var TerraformGVK = schema.GroupVersionKind{
	Group:   "infra.contrib.fluxcd.io",
	Version: "v1alpha2",
	Kind:    "Terraform",
}

// GitRepositoryGVK identifies the source-controller GitRepository custom
// resource referenced by a Terraform object's spec.sourceRef.
var GitRepositoryGVK = schema.GroupVersionKind{
	Group:   "source.toolkit.fluxcd.io",
	Version: "v1",
	Kind:    "GitRepository",
}

const (
	labelBranchPlanner   = "infra.weave.works/branch-planner"
	labelPullRequestID   = "infra.weave.works/pr-id"
	labelPrimaryResource = "infra.weave.works/primary-resource"

	annotationReview = "floor.osinfra.io/review"
)

// Workspace is the flattened, typed view of a Terraform object that
// floor-api exposes to callers.
type Workspace struct {
	Name                string
	Namespace           string
	Path                string
	SourceBranch        string
	Phase               string
	PendingPlan         string
	LastAppliedRevision string
	Review              string
	Destroy             bool
	PlanOnly            bool
	PullRequest         int
	PrimaryResource     string
}
