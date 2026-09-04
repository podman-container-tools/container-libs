//go:build !windows

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	imgcopy "go.podman.io/image/v5/copy"
	"go.podman.io/image/v5/signature"
	istorage "go.podman.io/image/v5/storage"
	"go.podman.io/image/v5/transports/alltransports"
	"go.podman.io/image/v5/types"
	"go.podman.io/storage"
	"go.podman.io/storage/pkg/reexec"
	storagetypes "go.podman.io/storage/types"

	jsonproxy "go.podman.io/common/pkg/json-proxy"
)

func main() {
	if reexec.Init() {
		return
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	sockfd := flag.Int("sockfd", -1, "socket file descriptor")
	policyPath := flag.String("policy", "", "path to policy.json (default: system default)")
	overrideArch := flag.String("override-arch", "", "override architecture for manifest list resolution")
	graphRoot := flag.String("graph-root", "", "storage graph root")
	runRoot := flag.String("run-root", "", "storage run root")
	seedImage := flag.String("seed-image", "", "image to copy into local store")
	flag.Parse()

	if *sockfd < 0 {
		return fmt.Errorf("usage: %s --sockfd <fd> [--policy <path>] [--override-arch <arch>] [--graph-root <path> --run-root <path> --seed-image <ref>]", os.Args[0])
	}

	if *graphRoot != "" {
		ref, store, err := setupStore(*graphRoot, *runRoot, *seedImage)
		if err != nil {
			return fmt.Errorf("setting up store: %w", err)
		}
		defer func() {
			_, _ = store.Shutdown(true)
		}()
		// Print the containers-storage:// reference for the test to read.
		fmt.Fprintln(os.Stdout, ref)
	}

	manager, err := jsonproxy.NewManager(
		jsonproxy.WithSystemContext(func() (*types.SystemContext, error) {
			sc := &types.SystemContext{}
			if *overrideArch != "" {
				sc.ArchitectureChoice = *overrideArch
			}
			return sc, nil
		}),
		jsonproxy.WithPolicyContext(func() (*signature.PolicyContext, error) {
			var policy *signature.Policy
			var err error
			if *policyPath != "" {
				policy, err = signature.NewPolicyFromFile(*policyPath)
			} else {
				policy, err = signature.DefaultPolicy(nil)
			}
			if err != nil {
				return nil, err
			}
			return signature.NewPolicyContext(policy)
		}),
	)
	if err != nil {
		return err
	}
	defer manager.Close()
	return manager.Serve(context.Background(), *sockfd)
}

func setupStore(graphRoot, runRoot, seedImage string) (string, storage.Store, error) {
	store, err := storage.GetStore(storagetypes.StoreOptions{
		GraphRoot:       graphRoot,
		RunRoot:         runRoot,
		GraphDriverName: "overlay",
	})
	if err != nil {
		return "", nil, fmt.Errorf("creating store: %w", err)
	}

	ctx := context.Background()

	srcRef, err := alltransports.ParseImageName(seedImage)
	if err != nil {
		return "", nil, fmt.Errorf("parsing seed image %q: %w", seedImage, err)
	}

	destRef, err := istorage.Transport.ParseStoreReference(store, "testimage:latest")
	if err != nil {
		return "", nil, fmt.Errorf("creating store reference: %w", err)
	}

	policy, err := signature.DefaultPolicy(nil)
	if err != nil {
		return "", nil, fmt.Errorf("getting default policy: %w", err)
	}
	pc, err := signature.NewPolicyContext(policy)
	if err != nil {
		return "", nil, fmt.Errorf("creating policy context: %w", err)
	}
	defer func() {
		if err := pc.Destroy(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: destroying policy context: %v\n", err)
		}
	}()

	_, err = imgcopy.Image(ctx, pc, destRef, srcRef, nil)
	if err != nil {
		return "", nil, fmt.Errorf("copying seed image: %w", err)
	}

	return "containers-storage:" + destRef.StringWithinTransport(), store, nil
}
