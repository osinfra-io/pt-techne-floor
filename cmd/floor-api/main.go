// Command floor-api serves the factory floor MCP tools over streamable
// HTTP, reading tofu-controller Terraform objects and approving their
// pending plans on behalf of the authenticated caller.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/osinfra-io/pt-techne-floor/internal/floor"
	"github.com/osinfra-io/pt-techne-floor/internal/githubapi"
	"github.com/osinfra-io/pt-techne-floor/internal/sandbox"
	"github.com/osinfra-io/pt-techne-floor/internal/server"
)

const (
	// listenAddress is where the MCP and health endpoints are served.
	listenAddress = ":8080"

	// sandboxInterval is how often the sandbox is checked for a closed
	// pull request it should be returned from.
	sandboxInterval = 30 * time.Second

	// shutdownTimeout bounds the graceful shutdown of the HTTP server.
	shutdownTimeout = 10 * time.Second
)

// version is set at build time from the release tag.
var version = "dev"

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// newScheme registers the Terraform and GitRepository GVKs as
// unstructured so the client can read and patch them without their Go
// types.
func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()

	for _, gvk := range []schema.GroupVersionKind{floor.TerraformGVK, floor.GitRepositoryGVK} {
		s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		s.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}

	return s
}

// run wires the cluster clients, the MCP server, and the HTTP endpoints,
// then serves until the process is asked to stop.
func run() error {
	approverGroup := os.Getenv("FLOOR_APPROVER_GROUP")
	gitRepository := os.Getenv("FLOOR_GIT_REPOSITORY")
	namespace := os.Getenv("FLOOR_NAMESPACE")

	for name, value := range map[string]string{
		"FLOOR_APPROVER_GROUP": approverGroup,
		"FLOOR_GIT_REPOSITORY": gitRepository,
		"FLOOR_NAMESPACE":      namespace,
	} {
		if value == "" {
			return fmt.Errorf("%s must be set", name)
		}
	}

	owner, repo, ok := strings.Cut(os.Getenv("FLOOR_GITHUB_REPO"), "/")
	if !ok {
		return errors.New("FLOOR_GITHUB_REPO must be set to owner/repo")
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("reading the in-cluster configuration: %w", err)
	}

	s := newScheme()

	k, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		return fmt.Errorf("building the cluster client: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("building the Kubernetes clientset: %w", err)
	}

	// The token is usually mounted from a file, which often ends in a newline.
	gh, err := githubapi.NewClient(strings.TrimSpace(os.Getenv("GITHUB_TOKEN")), owner, repo, "")
	if err != nil {
		return fmt.Errorf("building the GitHub client: %w", err)
	}

	broadcaster := record.NewBroadcaster()
	broadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: clientset.CoreV1().Events("")})

	defer broadcaster.Shutdown()

	additionalNamespaces := strings.FieldsFunc(os.Getenv("FLOOR_ADDITIONAL_NAMESPACES"), func(r rune) bool { return r == ',' })
	for i := range additionalNamespaces {
		additionalNamespaces[i] = strings.TrimSpace(additionalNamespaces[i])
		if additionalNamespaces[i] == "" {
			return errors.New("FLOOR_ADDITIONAL_NAMESPACES must not contain blank namespaces")
		}
	}

	fl := floor.NewClient(k, namespace, additionalNamespaces...)
	// PR repointing belongs only to the configured fixture repository.
	sb := sandbox.NewManager(floor.NewClient(k, namespace), gh, k, namespace, gitRepository)
	recorder := broadcaster.NewRecorder(s, corev1.EventSource{Component: "floor-api"})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go sb.Run(ctx, sandboxInterval)

	mux := http.NewServeMux()
	mux.Handle("/mcp", server.Handler(server.New(fl, sb, recorder, approverGroup)))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	httpServer := &http.Server{Addr: listenAddress, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		<-ctx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutting down: %v", err)
		}
	}()

	log.Printf("floor-api %s serving %s for namespace %s and additional namespaces %v", version, listenAddress, namespace, additionalNamespaces)

	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serving: %w", err)
	}

	return nil
}
