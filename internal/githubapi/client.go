// Package githubapi wraps go-github with the narrow surface the sandbox
// manager needs: reading a pull request's head and posting or updating a
// single marked comment on it.
package githubapi

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	gh "github.com/google/go-github/v68/github"
)

// PR is the minimal view of a pull request the sandbox manager needs.
type PR struct {
	HeadSHA string
	HeadRef string
	State   string
	Merged  bool
}

// Client is a GitHub client scoped to a single repository.
type Client struct {
	gh    *gh.Client
	owner string
	repo  string
}

// NewClient returns a Client authenticated with token for owner/repo.
// When baseURL is non-empty it is used verbatim instead of github.com,
// which is how tests point the client at an httptest server; an
// unparseable or hostless baseURL is an error rather than a silent
// fallback to github.com, which would send the token there.
func NewClient(token, owner, repo, baseURL string) (*Client, error) {
	client := gh.NewClient(nil).WithAuthToken(token)

	if baseURL != "" {
		if !strings.HasSuffix(baseURL, "/") {
			baseURL += "/"
		}

		parsed, err := url.Parse(baseURL)
		if err != nil {
			return nil, fmt.Errorf("parsing GitHub base URL %q: %w", baseURL, err)
		}

		if parsed.Host == "" {
			return nil, fmt.Errorf("GitHub base URL %q has no host", baseURL)
		}

		client.BaseURL, client.UploadURL = parsed, parsed
	}

	return &Client{gh: client, owner: owner, repo: repo}, nil
}

// PullRequest returns the head, state, and merged flag of a pull request.
func (c *Client) PullRequest(ctx context.Context, number int) (PR, error) {
	pr, _, err := c.gh.PullRequests.Get(ctx, c.owner, c.repo, number)
	if err != nil {
		return PR{}, fmt.Errorf("getting pull request %d: %w", number, err)
	}

	return PR{
		HeadSHA: pr.GetHead().GetSHA(),
		HeadRef: pr.GetHead().GetRef(),
		State:   pr.GetState(),
		Merged:  pr.GetMerged(),
	}, nil
}

// Comment posts a new comment on a pull request.
func (c *Client) Comment(ctx context.Context, number int, body string) error {
	comment := &gh.IssueComment{Body: gh.Ptr(body)}

	if _, _, err := c.gh.Issues.CreateComment(ctx, c.owner, c.repo, number, comment); err != nil {
		return fmt.Errorf("creating comment on pull request %d: %w", number, err)
	}

	return nil
}

// UpsertComment edits the first comment on the pull request containing
// marker, or creates a new one when no such comment exists. The body must
// contain the marker so later calls find it again.
func (c *Client) UpsertComment(ctx context.Context, number int, marker, body string) error {
	id, err := c.findComment(ctx, number, marker)
	if err != nil {
		return err
	}

	if id == 0 {
		return c.Comment(ctx, number, body)
	}

	comment := &gh.IssueComment{Body: gh.Ptr(body)}

	if _, _, err := c.gh.Issues.EditComment(ctx, c.owner, c.repo, id, comment); err != nil {
		return fmt.Errorf("editing comment %d on pull request %d: %w", id, number, err)
	}

	return nil
}

// findComment returns the id of the first comment on the pull request
// whose body contains marker, or 0 when there is none.
func (c *Client) findComment(ctx context.Context, number int, marker string) (int64, error) {
	options := &gh.IssueListCommentsOptions{ListOptions: gh.ListOptions{PerPage: 100}}

	for {
		comments, response, err := c.gh.Issues.ListComments(ctx, c.owner, c.repo, number, options)
		if err != nil {
			return 0, fmt.Errorf("listing comments on pull request %d: %w", number, err)
		}

		for _, comment := range comments {
			if strings.Contains(comment.GetBody(), marker) {
				return comment.GetID(), nil
			}
		}

		if response.NextPage == 0 {
			return 0, nil
		}

		options.Page = response.NextPage
	}
}
