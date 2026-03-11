// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/aws-ia/cfn-aws-soci-index-builder/soci-index-generator-lambda/events"
	"github.com/aws/aws-lambda-go/lambdacontext"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/images"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// This test ensures that the handler can pull Docker and OCI images, build, and push the SOCI index back to the repository.
// To run this test locally, you need to push an image to a private ECR repository, and set following environment variables:
// AWS_ACCOUNT_ID: your aws account id.
// AWS_REGION: the region of your private ECR repository.
// REPOSITORY_NAME: name of your private ECR repository.
// DOCKER_IMAGE_DIGEST: the digest of your image.
// OCI_IMAGE_DIGEST: the digest of your OCI image.
func TestHandlerHappyPath(t *testing.T) {
	// Test with both V1 and V2 SOCI index versions
	testVersions := []string{"V1", "V2"}

	for _, version := range testVersions {
		t.Run("SOCI Index Version "+version, func(t *testing.T) {
			// Set the SOCI index version environment variable
			if err := os.Setenv("soci_index_version", version); err != nil {
				t.Fatalf("Failed to set environment variable: %v", err)
			}
			t.Logf("Testing with SOCI index version: %s", version)

			doTest := func(imageDigest string) {
				event := events.ECRImageActionEvent{
					Version:    "1",
					Id:         "id",
					DetailType: "ECR Image Action",
					Source:     "aws.ecr",
					Account:    os.Getenv("AWS_ACCOUNT_ID"),
					Time:       "time",
					Region:     os.Getenv("AWS_REGION"),
					Detail: events.ECRImageActionEventDetail{
						ActionType:     "PUSH",
						Result:         "SUCCESS",
						RepositoryName: os.Getenv("REPOSITORY_NAME"),
						ImageDigest:    imageDigest,
						ImageTag:       "test",
					},
				}

				// making the test context
				lc := lambdacontext.LambdaContext{}
				lc.AwsRequestID = "request-id-" + imageDigest + "-" + version
				ctx := lambdacontext.NewContext(context.Background(), &lc)
				ctx, cancel := context.WithDeadline(ctx, time.Now().Add(time.Minute))
				defer cancel()

				resp, err := HandleRequest(ctx, event)
				if err != nil {
					t.Fatalf("HandleRequest failed with version %s: %v", version, err)
				}

				expected_resp := "Successfully built and pushed SOCI index"
				if resp != expected_resp {
					t.Fatalf("Unexpected response with version %s. Expected %s but got %s", version, expected_resp, resp)
				}
			}

			doTest(os.Getenv("DOCKER_IMAGE_DIGEST"))
			doTest(os.Getenv("OCI_IMAGE_DIGEST"))
		})
	}
}

// This test ensures that the handler can validate the input digest media type
// To run this test locally, you need to push an image to a private ECR repository, and set following environment variables:
// AWS_ACCOUNT_ID: your aws account id.
// AWS_REGION: the region of your private ECR repository.
// REPOSITORY_NAME: name of your private ECR repository.
// INVALID_IMAGE_DIGEST: the digest of anything that isn't an image.
func TestHandlerInvalidDigestMediaType(t *testing.T) {
	// Test with both V1 and V2 SOCI index versions
	testVersions := []string{"V1", "V2"}

	for _, version := range testVersions {
		t.Run("SOCI Index Version "+version, func(t *testing.T) {
			// Set the SOCI index version environment variable
			if err := os.Setenv("soci_index_version", version); err != nil {
				t.Fatalf("Failed to set environment variable: %v", err)
			}
			t.Logf("Testing with SOCI index version: %s", version)

			event := events.ECRImageActionEvent{
				Version:    "1",
				Id:         "id",
				DetailType: "ECR Image Action",
				Source:     "aws.ecr",
				Account:    os.Getenv("AWS_ACCOUNT_ID"),
				Time:       "time",
				Region:     os.Getenv("AWS_REGION"),
				Detail: events.ECRImageActionEventDetail{
					ActionType:     "PUSH",
					Result:         "SUCCESS",
					RepositoryName: os.Getenv("REPOSITORY_NAME"),
					ImageDigest:    os.Getenv("INVALID_IMAGE_DIGEST"),
					ImageTag:       "test",
				},
			}

			// making the test context
			lc := lambdacontext.LambdaContext{}
			lc.AwsRequestID = "abcd-1234-" + version
			ctx := lambdacontext.NewContext(context.Background(), &lc)
			ctx, cancel := context.WithDeadline(ctx, time.Now().Add(time.Minute))
			defer cancel()

			resp, err := HandleRequest(ctx, event)
			if err != nil {
				t.Fatalf("Invalid image digest is not expected to fail with version %s", version)
			}

			expected_resp := "Exited early due to manifest validation error"
			if resp != expected_resp {
				t.Fatalf("Unexpected response with version %s. Expected %s but got %s", version, expected_resp, resp)
			}
		})
	}
}

// TestImagePlatformsArm64Only verifies that images.Platforms returns the platform(s)
// present in an OCI image index (e.g. linux/arm64 only), so that the handler's V2 path
// can pass them to ConvertWithPlatforms and work regardless of Lambda runtime architecture.
// Note: building this package requires Linux (C deps in soci-snapshotter); run via make test in CI.
func TestImagePlatformsArm64Only(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := local.NewStore(dir)
	if err != nil {
		t.Fatalf("local.NewStore: %v", err)
	}

	// Build a minimal OCI image index with a single linux/arm64 manifest entry.
	// We only need the index blob; the child manifest digest is never read because
	// the Platforms walk sees Platform set and appends it then skips descendants.
	arm64 := ocispec.Platform{OS: "linux", Architecture: "arm64"}
	dummyManifestDigest := digest.FromString("dummy-manifest-for-arm64")
	index := ocispec.Index{
		Versioned: specs.Versioned{SchemaVersion: 2},
		Manifests: []ocispec.Descriptor{
			{
				MediaType: ocispec.MediaTypeImageManifest,
				Digest:    dummyManifestDigest,
				Size:      0,
				Platform:  &arm64,
			},
		},
	}
	indexJSON, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("json.Marshal index: %v", err)
	}
	indexDigest := digest.FromBytes(indexJSON)
	indexDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageIndex,
		Digest:    indexDigest,
		Size:      int64(len(indexJSON)),
	}

	ref := "test-arm64-index"
	if err := content.WriteBlob(ctx, store, ref, bytes.NewReader(indexJSON), indexDesc); err != nil {
		t.Fatalf("WriteBlob index: %v", err)
	}

	// Same logic as handler's V2 path: resolve platforms from the image.
	platforms, err := images.Platforms(ctx, store, indexDesc)
	if err != nil {
		t.Fatalf("images.Platforms: %v", err)
	}
	if len(platforms) == 0 {
		t.Fatal("expected at least one platform for arm64-only index")
	}
	if len(platforms) != 1 {
		t.Fatalf("expected exactly one platform, got %d: %v", len(platforms), platforms)
	}
	if platforms[0].OS != "linux" || platforms[0].Architecture != "arm64" {
		t.Fatalf("expected linux/arm64, got %s/%s", platforms[0].OS, platforms[0].Architecture)
	}
}

// TestImagePlatformsEmptyIndex ensures we get no runnable platforms when the index
// only has attestation (unknown platform) entries, matching images.Platforms behavior.
func TestImagePlatformsEmptyIndex(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := local.NewStore(dir)
	if err != nil {
		t.Fatalf("local.NewStore: %v", err)
	}

	unknown := ocispec.Platform{OS: "unknown", Architecture: "unknown"}
	dummyDigest := digest.FromString("dummy-attestation")
	index := ocispec.Index{
		Versioned: specs.Versioned{SchemaVersion: 2},
		Manifests: []ocispec.Descriptor{
			{
				MediaType: "application/vnd.oci.image.manifest.v1+json",
				Digest:    dummyDigest,
				Size:      0,
				Platform:  &unknown,
			},
		},
	}
	indexJSON, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("json.Marshal index: %v", err)
	}
	indexDigest := digest.FromBytes(indexJSON)
	indexDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageIndex,
		Digest:    indexDigest,
		Size:      int64(len(indexJSON)),
	}

	ref := "test-attestation-only-index"
	if err := content.WriteBlob(ctx, store, ref, bytes.NewReader(indexJSON), indexDesc); err != nil {
		t.Fatalf("WriteBlob index: %v", err)
	}

	platforms, err := images.Platforms(ctx, store, indexDesc)
	if err != nil {
		t.Fatalf("images.Platforms: %v", err)
	}
	if len(platforms) != 0 {
		t.Fatalf("expected no runnable platforms for attestation-only index, got %d: %v", len(platforms), platforms)
	}
}
