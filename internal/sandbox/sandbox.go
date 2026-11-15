// Package sandbox repoints the sandbox GitRepository at a pull request's
// head so tofu-controller plans the proposed change, approves that plan
// once it is confirmed to be for the right revision, and returns the
// GitRepository to main when the pull request closes.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/osinfra-io/pt-techne-floor/internal/floor"
	"github.com/osinfra-io/pt-techne-floor/internal/githubapi"
)

const (
	// annotationPullRequest records which pull request the sandbox
	// GitRepository is currently pointed at.
	annotationPullRequest = "floor.osinfra.io/pull-request"

	// commentMarker identifies the single sandbox comment floor-api
	// maintains on a pull request.
	commentMarker = "<!-- floor:sandbox -->"

	// fieldOwner is the server-side apply field manager floor-api uses.
	fieldOwner = "floor-api"

	// mainBranch is the branch the sandbox GitRepository returns to.
	mainBranch = "main"

	// revisionPrefixLength is the number of revision characters
	// tofu-controller appends to a plan id.
	revisionPrefixLength = 10
)

// ErrPullRequestNotOpen is returned when the pull request behind a plan
// is closed or already merged.
var ErrPullRequestNotOpen = errors.New("pull request is not open")

// ErrRevisionTimeout is returned when the primary Terraform object does
// not produce a plan for the pull request head within RevisionTimeout.
var ErrRevisionTimeout = errors.New("timed out waiting for a plan at the pull request head revision")

// ErrRevisionMismatch is returned when the primary Terraform object has a
// pending plan for a revision other than the pull request head.
type ErrRevisionMismatch struct {
	Expected string
	Observed string
}

func (e ErrRevisionMismatch) Error() string {
	return fmt.Sprintf("pending plan %q is not for revision %q", e.Observed, e.Expected)
}

// Manager owns the sandbox GitRepository: it repoints it at pull request
// heads for review and returns it to main afterwards.
type Manager struct {
	// PollInterval is how often the primary Terraform object is read
	// while waiting for a plan at the pull request head.
	PollInterval time.Duration

	// RevisionTimeout bounds that wait.
	RevisionTimeout time.Duration

	floor         *floor.Client
	github        *githubapi.Client
	k             client.Client
	namespace     string
	gitRepository string
}

// NewManager returns a Manager for the named GitRepository in namespace.
func NewManager(fl *floor.Client, gh *githubapi.Client, k client.Client, namespace, gitRepository string) *Manager {
	return &Manager{
		PollInterval:    2 * time.Second,
		RevisionTimeout: 180 * time.Second,
		floor:           fl,
		github:          gh,
		k:               k,
		namespace:       namespace,
		gitRepository:   gitRepository,
	}
}

// ApprovePullRequestPlan approves planID, which a human read on the
// planner Terraform object, by repointing the sandbox GitRepository at
// the pull request head, waiting for the primary Terraform object to plan
// that same revision, and approving the primary's pending plan. It
// returns the primary's plan id that was approved.
func (m *Manager) ApprovePullRequestPlan(ctx context.Context, plannerName, planID string, acknowledgeDestroys bool, approver string) (string, error) {
	planner, err := m.floor.Get(ctx, plannerName)
	if err != nil {
		return "", err
	}

	if err := floor.ValidateApproval(planner, planID, acknowledgeDestroys); err != nil {
		return "", err
	}

	if planner.PullRequest == 0 {
		return "", fmt.Errorf("terraform object %q has no pull request label", plannerName)
	}

	if planner.PrimaryResource == "" {
		return "", fmt.Errorf("terraform object %q has no primary resource label", plannerName)
	}

	pr, err := m.github.PullRequest(ctx, planner.PullRequest)
	if err != nil {
		return "", err
	}

	if pr.State != "open" || pr.Merged {
		return "", fmt.Errorf("%w: pull request %d is %q (merged: %t)", ErrPullRequestNotOpen, planner.PullRequest, pr.State, pr.Merged)
	}

	if len(pr.HeadSHA) < revisionPrefixLength {
		return "", fmt.Errorf("pull request %d head revision %q is too short", planner.PullRequest, pr.HeadSHA)
	}

	revisionPrefix := pr.HeadSHA[:revisionPrefixLength]

	// The pull request can receive a new commit before the planner
	// refreshes its plan, so the plan the human read must be for the head
	// revision; otherwise they would approve unreviewed changes.
	if !strings.HasSuffix(planID, revisionPrefix) {
		return "", ErrRevisionMismatch{Expected: revisionPrefix, Observed: planID}
	}

	// The primary normally carries an unapproved plan for main, which
	// survives the repoint until the planner re-plans at the pull request
	// head; it is a stale observation, not a revision mismatch.
	primary, err := m.floor.Get(ctx, planner.PrimaryResource)
	if err != nil {
		return "", err
	}

	stalePlan := primary.PendingPlan

	if err := m.repointGitRepository(ctx, pr.HeadRef, strconv.Itoa(planner.PullRequest)); err != nil {
		return "", err
	}

	pendingPlan, err := m.waitForRevision(ctx, planner.PrimaryResource, revisionPrefix, stalePlan)
	if err != nil {
		return "", err
	}

	if err := m.floor.Approve(ctx, planner.PrimaryResource, pendingPlan, acknowledgeDestroys); err != nil {
		return "", err
	}

	body := fmt.Sprintf(
		"%s\nSandbox plan **%s** for `%s` approved by @%s at `%s`.",
		commentMarker, pendingPlan, planner.PrimaryResource, approver, pr.HeadSHA,
	)

	// The approval is already written, so the comment is post-approval
	// telemetry: a failure is logged, not returned.
	if err := m.github.UpsertComment(ctx, planner.PullRequest, commentMarker, body); err != nil {
		log.Printf("commenting on pull request %d: %v", planner.PullRequest, err)
	}

	return pendingPlan, nil
}

// ReturnToMainIfClosed points the sandbox GitRepository back at main when
// the pull request it is currently pointed at is closed or merged.
func (m *Manager) ReturnToMainIfClosed(ctx context.Context) (bool, error) {
	repo, err := m.getGitRepository(ctx)
	if err != nil {
		return false, err
	}

	annotation := repo.GetAnnotations()[annotationPullRequest]
	if annotation == "" {
		return false, nil
	}

	number, err := strconv.Atoi(annotation)
	if err != nil {
		return false, fmt.Errorf("GitRepository %q has an invalid %s annotation %q: %w", m.gitRepository, annotationPullRequest, annotation, err)
	}

	pr, err := m.github.PullRequest(ctx, number)
	if err != nil {
		return false, err
	}

	if pr.State == "open" && !pr.Merged {
		return false, nil
	}

	// The pull request check is a round trip to GitHub, so an approval may
	// have repointed the GitRepository at another pull request meanwhile.
	// The branch change and the annotation removal are one patch guarded by
	// the resource version observed above; a conflict means a newer
	// selection won and the next poll re-reads it.
	if err := m.returnToMain(ctx, repo); err != nil {
		if apierrors.IsConflict(err) {
			return false, nil
		}

		return false, err
	}

	return true, nil
}

// returnToMain points the sandbox GitRepository back at main and removes
// the pull request annotation in a single optimistically locked patch
// built from observed.
func (m *Manager) returnToMain(ctx context.Context, observed *unstructured.Unstructured) error {
	patch := client.MergeFromWithOptions(observed.DeepCopy(), client.MergeFromWithOptimisticLock{})

	updated := observed.DeepCopy()

	if err := unstructured.SetNestedField(updated.Object, mainBranch, "spec", "ref", "branch"); err != nil {
		return fmt.Errorf("setting GitRepository %q branch: %w", m.gitRepository, err)
	}

	annotations := updated.GetAnnotations()
	delete(annotations, annotationPullRequest)
	updated.SetAnnotations(annotations)

	if err := m.k.Patch(ctx, updated, patch); err != nil {
		return fmt.Errorf("returning GitRepository %q to %s: %w", m.gitRepository, mainBranch, err)
	}

	return nil
}

// Run calls ReturnToMainIfClosed every interval until ctx is cancelled.
func (m *Manager) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if _, err := m.ReturnToMainIfClosed(ctx); err != nil {
			log.Printf("returning sandbox GitRepository %q to %s: %v", m.gitRepository, mainBranch, err)
		}
	}
}

// waitForRevision polls the primary Terraform object until it has a
// pending plan for the given revision prefix, returning that plan id.
// stalePlan, the pending plan read before the GitRepository was
// repointed, is treated as "not re-planned yet" only when it is for a
// different revision: a retry can find the right plan already pending.
func (m *Manager) waitForRevision(ctx context.Context, primary, revisionPrefix, stalePlan string) (string, error) {
	deadline := time.Now().Add(m.RevisionTimeout)

	ticker := time.NewTicker(m.PollInterval)
	defer ticker.Stop()

	for {
		ws, err := m.floor.Get(ctx, primary)
		if err != nil {
			return "", err
		}

		if ws.PendingPlan != "" {
			if strings.HasSuffix(ws.PendingPlan, revisionPrefix) {
				return ws.PendingPlan, nil
			}

			if ws.PendingPlan != stalePlan {
				return "", ErrRevisionMismatch{Expected: revisionPrefix, Observed: ws.PendingPlan}
			}
		}

		if time.Now().After(deadline) {
			return "", fmt.Errorf("%w: Terraform object %q, revision %q", ErrRevisionTimeout, primary, revisionPrefix)
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

// getGitRepository reads the sandbox GitRepository.
func (m *Manager) getGitRepository(ctx context.Context) (*unstructured.Unstructured, error) {
	repo := &unstructured.Unstructured{}
	repo.SetGroupVersionKind(floor.GitRepositoryGVK)

	if err := m.k.Get(ctx, types.NamespacedName{Namespace: m.namespace, Name: m.gitRepository}, repo); err != nil {
		return nil, fmt.Errorf("getting GitRepository %q: %w", m.gitRepository, err)
	}

	return repo, nil
}

// repointGitRepository server-side applies the branch and the pull
// request annotation, forcing ownership under the floor-api field
// manager.
func (m *Manager) repointGitRepository(ctx context.Context, branch, pullRequest string) error {
	metadata := map[string]any{
		"name":      m.gitRepository,
		"namespace": m.namespace,
		"annotations": map[string]any{
			annotationPullRequest: pullRequest,
		},
	}

	patch := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": floor.GitRepositoryGVK.GroupVersion().String(),
			"kind":       floor.GitRepositoryGVK.Kind,
			"metadata":   metadata,
			"spec": map[string]any{
				"ref": map[string]any{
					"branch": branch,
				},
			},
		},
	}

	if err := m.k.Apply(ctx, client.ApplyConfigurationFromUnstructured(patch), client.FieldOwner(fieldOwner), client.ForceOwnership); err != nil {
		return fmt.Errorf("applying GitRepository %q: %w", m.gitRepository, err)
	}

	return nil
}
