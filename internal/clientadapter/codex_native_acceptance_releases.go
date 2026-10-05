package clientadapter

// Exact official npm compound releases used by native MCP acceptance. Only
// Darwin arm64 artifacts have been verified here; no other platform is implied.
// Source: rust-v0.159.2 (ff6aec96948b70d94983af2641a6b67c94faeff5),
// rust-v0.160.0 (a956835d020762cb2b570053af06f643a11c0ecc).
// Both accept the root CA and uncompressed Responses launch configuration.
// Their removed responses_websockets switch grants no feature flag here.
func CodexCLI01592DarwinARM64() Release {
	release := codexNativeMCPRelease("0.159.2",
		"d4e69a623f79ede30c5abb2a4a4bef8b7ff71dbda1362ebe198676ab56113c15",
		"73738c674e311dae00130ae6510a7d8ef9564dbb4ff588ad2d64cc10f267041e",
		"16593cc2f422d5f398a8e40f550ebbaf1245392528957be342c295920a300704")
	release.Revision = 2
	release.Features = FeatureResponsesDataKeepalive
	return release
}

func CodexCLI01600DarwinARM64() Release {
	release := codexNativeMCPRelease("0.160.0",
		"29c350dfcd8d33749852c16e2f5dcde528409d7e1d3f5d916fe3c576f3914820",
		"2497ae33d4df7cb43618e3935fb1dfbff6a7bf536e2258776d1e95afc3c47017",
		"112fae7a5a1223e673c8a1791d32338f37df8b527ff1159bb8adac6c4dbf1b4b")
	// Native DATA controls prove this exact tuple, not the shared factory.
	release.Revision = 2
	release.Features = FeatureResponsesDataKeepalive
	return release
}

func codexNativeMCPRelease(version, metadata, platformMetadata, native string) Release {
	return Release{
		ID: "codex-cli", Revision: 1, Version: version, OperatingSystem: "darwin", Architecture: "arm64",
		InstallShape: InstallNPMWrapperNativeChild, InvocationLabel: "codex", CanonicalEntrypointName: "codex.js", ArtifactRoot: "../..",
		Artifacts: []Artifact{
			{Role: ArtifactEntrypoint, SHA256: "61b0194f3bb6534439c8d26a3ed57d0805f84b884588b761795323eeb92fcf70"},
			{Role: ArtifactMainPackageMetadata, RelativePath: "../package.json", SHA256: metadata, MaxBytes: 1 << 20},
			{Role: ArtifactPlatformPackageMetadata, RelativePath: "../../codex-darwin-arm64/package.json", SHA256: platformMetadata, MaxBytes: 1 << 20},
			{Role: ArtifactNativeChild, RelativePath: "../../codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex", SHA256: native},
		},
		LaunchRecipe: LaunchCodexResponsesHTTP,
	}
}
