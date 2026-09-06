package platform

import (
	"fmt"
	"runtime"
	"testing"

	imgspecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"go.podman.io/image/v5/types"
)

func TestWantedPlatforms(t *testing.T) {
	for _, c := range []struct {
		ctx      types.SystemContext
		expected []imgspecv1.Platform
	}{
		{ // amd64 without variant accepts baseline only
			types.SystemContext{ArchitectureChoice: "amd64", OSChoice: "linux", DetectPlatformVariant: true},
			[]imgspecv1.Platform{
				{OS: "linux", Architecture: "amd64", Variant: ""},
			},
		},
		{ // amd64 with variant walks down from the requested level
			types.SystemContext{ArchitectureChoice: "amd64", OSChoice: "linux", VariantChoice: "v3"},
			[]imgspecv1.Platform{
				{OS: "linux", Architecture: "amd64", Variant: "v3"},
				{OS: "linux", Architecture: "amd64", Variant: "v2"},
				{OS: "linux", Architecture: "amd64", Variant: ""},
			},
		},
		{ // amd64 with v2 variant
			types.SystemContext{ArchitectureChoice: "amd64", OSChoice: "linux", VariantChoice: "v2"},
			[]imgspecv1.Platform{
				{OS: "linux", Architecture: "amd64", Variant: "v2"},
				{OS: "linux", Architecture: "amd64", Variant: ""},
			},
		},
		{ // amd64 with v1 produces the canonical baseline
			types.SystemContext{ArchitectureChoice: "amd64", OSChoice: "linux", VariantChoice: "v1"},
			[]imgspecv1.Platform{
				{OS: "linux", Architecture: "amd64", Variant: ""},
			},
		},
		{ // ARM with variant
			types.SystemContext{ArchitectureChoice: "arm", OSChoice: "linux", VariantChoice: "v6"},
			[]imgspecv1.Platform{
				{OS: "linux", Architecture: "arm", Variant: "v6"},
				{OS: "linux", Architecture: "arm", Variant: "v5"},
				{OS: "linux", Architecture: "arm", Variant: ""},
			},
		},
		{ // ARM without variant
			types.SystemContext{ArchitectureChoice: "arm", OSChoice: "linux"},
			[]imgspecv1.Platform{
				{OS: "linux", Architecture: "arm", Variant: ""},
				{OS: "linux", Architecture: "arm", Variant: "v8"},
				{OS: "linux", Architecture: "arm", Variant: "v7"},
				{OS: "linux", Architecture: "arm", Variant: "v6"},
				{OS: "linux", Architecture: "arm", Variant: "v5"},
			},
		},
		{ // ARM64 has a base variant
			types.SystemContext{ArchitectureChoice: "arm64", OSChoice: "linux"},
			[]imgspecv1.Platform{
				{OS: "linux", Architecture: "arm64", Variant: ""},
				{OS: "linux", Architecture: "arm64", Variant: "v8"},
			},
		},
		{ // Custom (completely unrecognized data)
			types.SystemContext{ArchitectureChoice: "armel", OSChoice: "freeBSD", VariantChoice: "custom"},
			[]imgspecv1.Platform{
				{OS: "freeBSD", Architecture: "armel", Variant: "custom"},
				{OS: "freeBSD", Architecture: "armel", Variant: ""},
			},
		},
	} {
		testName := fmt.Sprintf("%q/%q/%q", c.ctx.ArchitectureChoice, c.ctx.OSChoice, c.ctx.VariantChoice)
		platforms := WantedPlatforms(&c.ctx)
		assert.Equal(t, c.expected, platforms, testName)
	}
}

func TestMatchesPlatform(t *testing.T) {
	for _, c := range []struct {
		name     string
		image    imgspecv1.Platform
		wanted   imgspecv1.Platform
		expected bool
	}{
		{
			name:     "exact match",
			image:    imgspecv1.Platform{OS: "linux", Architecture: "amd64", Variant: "v3"},
			wanted:   imgspecv1.Platform{OS: "linux", Architecture: "amd64", Variant: "v3"},
			expected: true,
		},
		{
			name:     "amd64 v1 matches empty variant",
			image:    imgspecv1.Platform{OS: "linux", Architecture: "amd64", Variant: "v1"},
			wanted:   imgspecv1.Platform{OS: "linux", Architecture: "amd64", Variant: ""},
			expected: true,
		},
		{
			name:     "amd64 empty variant matches v1",
			image:    imgspecv1.Platform{OS: "linux", Architecture: "amd64", Variant: ""},
			wanted:   imgspecv1.Platform{OS: "linux", Architecture: "amd64", Variant: "v1"},
			expected: true,
		},
		{
			name:     "amd64 v3 does not match v2",
			image:    imgspecv1.Platform{OS: "linux", Architecture: "amd64", Variant: "v3"},
			wanted:   imgspecv1.Platform{OS: "linux", Architecture: "amd64", Variant: "v2"},
			expected: false,
		},
		{
			name:     "arm v1 normalization does not apply",
			image:    imgspecv1.Platform{OS: "linux", Architecture: "arm", Variant: "v1"},
			wanted:   imgspecv1.Platform{OS: "linux", Architecture: "arm", Variant: ""},
			expected: false,
		},
		{
			name:     "different OS does not match",
			image:    imgspecv1.Platform{OS: "windows", Architecture: "amd64", Variant: ""},
			wanted:   imgspecv1.Platform{OS: "linux", Architecture: "amd64", Variant: ""},
			expected: false,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.expected, MatchesPlatform(c.image, c.wanted))
		})
	}
}

func TestClassifyAmd64Variant(t *testing.T) {
	// Baseline flags present on all x86-64 CPUs (SSE2, etc.).
	// These are not checked because every x86-64 CPU has them.
	baseFlags := map[string]bool{
		"fpu": true, "sse": true, "sse2": true, "mmx": true, "fxsr": true,
		"syscall": true, "nx": true, "lm": true,
	}

	makeFlags := func(extra ...string) map[string]bool {
		flags := make(map[string]bool, len(baseFlags)+len(extra))
		for k, v := range baseFlags {
			flags[k] = v
		}
		for _, f := range extra {
			flags[f] = true
		}
		return flags
	}

	v2Flags := []string{"cx16", "lahf_lm", "popcnt", "pni", "sse4_1", "sse4_2", "ssse3"}
	v3Extra := []string{"avx", "avx2", "bmi1", "bmi2", "f16c", "fma", "abm", "movbe", "xsave"}
	v4Extra := []string{"avx512f", "avx512bw", "avx512cd", "avx512dq", "avx512vl"}

	allV3 := append(append([]string{}, v2Flags...), v3Extra...)
	allV4 := append(append([]string{}, allV3...), v4Extra...)

	for _, c := range []struct {
		name     string
		flags    map[string]bool
		expected string
	}{
		{"empty flags returns v1", map[string]bool{}, "v1"},
		{"baseline-only returns v1", baseFlags, "v1"},
		{"nil flags returns v1", nil, "v1"},
		{"all v2 flags returns v2", makeFlags(v2Flags...), "v2"},
		{"v2 missing popcnt returns v1", makeFlags("cx16", "lahf_lm", "pni", "sse4_1", "sse4_2", "ssse3"), "v1"},
		{"v2 missing cx16 returns v1", makeFlags("lahf_lm", "popcnt", "pni", "sse4_1", "sse4_2", "ssse3"), "v1"},
		{"all v3 flags returns v3", makeFlags(allV3...), "v3"},
		{"v3 missing avx2 returns v2", makeFlags(append(v2Flags, "avx", "bmi1", "bmi2", "f16c", "fma", "abm", "movbe", "xsave")...), "v2"},
		{"v3 missing fma returns v2", makeFlags(append(v2Flags, "avx", "avx2", "bmi1", "bmi2", "f16c", "abm", "movbe", "xsave")...), "v2"},
		{"all v4 flags returns v4", makeFlags(allV4...), "v4"},
		{"v4 missing avx512vl returns v3", makeFlags(append(allV3, "avx512f", "avx512bw", "avx512cd", "avx512dq")...), "v3"},
		{"v4 missing avx512f returns v3", makeFlags(append(allV3, "avx512bw", "avx512cd", "avx512dq", "avx512vl")...), "v3"},
	} {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.expected, classifyAmd64Variant(c.flags))
		})
	}
}

func TestDetectPlatformVariant(t *testing.T) {
	if runtime.GOARCH != "amd64" || runtime.GOOS != "linux" {
		t.Skip("amd64/linux-only test")
	}

	baseline := WantedPlatforms(new(types.SystemContext))
	assert.Equal(t, []imgspecv1.Platform{
		{OS: runtime.GOOS, Architecture: "amd64", Variant: ""},
	}, baseline, "without DetectPlatformVariant, only baseline is returned")

	detected := WantedPlatforms(&types.SystemContext{DetectPlatformVariant: true})
	expectedVariant := normalizeAmd64Variant("amd64", getCPUVariant(runtime.GOOS, runtime.GOARCH))
	assert.Equal(t, expectedVariant, detected[0].Variant, "first entry should match the detected variant")
	assert.Empty(t, detected[len(detected)-1].Variant, "last entry should be the baseline fallback")
	if expectedVariant == "" {
		assert.Equal(t, baseline, detected, "v1 detection should produce only the canonical baseline")
	} else {
		assert.Greater(t, len(detected), 1, "higher variants should include a baseline fallback")
	}
}

func TestShouldDetectVariant(t *testing.T) {
	for _, c := range []struct {
		arch               string
		detectAmd64Variant bool
		expected           bool
	}{
		{"amd64", false, false},
		{"amd64", true, true},
		{"arm", false, true},
		{"arm64", false, true},
	} {
		assert.Equal(t, c.expected, shouldDetectVariant(c.arch, c.detectAmd64Variant), c.arch)
	}
}
