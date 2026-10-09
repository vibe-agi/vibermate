# Go Security Patch Before 0.1.25 Publication

> **For agentic workers:** Use superpowers:subagent-driven-development and systematic-debugging; retain actual CI RED and verify the patched toolchain/dependencies before claiming GREEN.

**Goal:** Remove the actual reachable Go/std-library and x/net vulnerability findings blocking the 0.1.25 candidate, without weakening security scans or source-provenance pins.

**Architecture:** Stay on Go1.26 and apply official patch1.26.9. Update x/net to its minimum fixed0.60.0 and only the transitive versions it requires. Keep the exact acceptance/build toolchain identity synchronized; historical source provenance remains unchanged. No HTTP/model/auth redesign.

**Tech Stack:** Go1.26.9, x/net0.60.0; existingNode22.23.1/Flutterpins unchanged.

**Spec/evidence:** Actual PR46 candidatefa4ec09, CI37976295645 vulnerability job113975538138 and native-release job113975538152. OfficialGo database https://vuln.go.dev/ID/GO-2026-6617.json, download metadata https://go.dev/dl/?mode=json, module https://proxy.golang.org/golang.org/x/net/@v/v0.60.0.mod. Raw copies under sibling login-fixes-releaseSDD. This observed release/security requirement supersedes the prior bounded tasks' no-toolchain/dependency-upgrade scheduling restriction only for these necessary patches.

## Global constraints

- Stableworktreeonly; originalfa4ec09candidate sourcefrozen, no published0.1.25assets exist. Preserve allfunctionalfixes/userdata/trust/runningapps; no sourcewriter overlap with UDPtask. Rootmusthandoffafterits safecheckpoint.
- Never suppress vulnerabilities, skip scanner, widen ExpectedGoVersion to arange, or lower source/hash/signatureverification. No globalHomebrewGo/OSconfiguration replacement.
- Preserve actualCIerrors. Report what isreachableversusmodule-only and Windows-only; don't inferexploitation orcleanliness fromadvisorycountalone.
- Do not alter historicalreports, publishedreleaseversions, or provenance comments forcodeadaptedfromGo1.26.8 (e.g. exchange_content_json_scan.go). New buildpinschange; oldsourceattributiondoesnot.

## Task 1: Minimal supported patch and exact provenance pins

**Files:** go.mod/go.sum; exact currentExpectedGoVersion andfixtures ininternal/acceptancereport; .github/workflows/ci.yml exacttoolchain assertion; presentbuild-versionfixtures underui/flutter_app/tool andtool/; Dockerbase/toolchainonlyifconcreteconsumerrequires. Releasev0.1.25notes getashortsecuritypatchline, notnewfeatureclaims.

**Interfaces:** Go moduletoolchain stays exactgo1.26.9; generatedbuildmanifest/verifieradmit the same exacttoolchain fordaemon/launcher/acceptance. x/net0.60.0 go.mod requiresgo1.26.0 andx/crypto0.57.0,x/sys0.48.0,x/term0.46.0,x/text0.42.0. Existingversions arelower; allow these minimalMVSbumps, not `go get -u ./...`.

- [ ] Read retainedCIlogs (ordinary+native) andofficialpatchedversions. Acquire officialGo1.26.9 inisolatedtaskdirectory ornormalversionedGo toolchaincache; validate officialchecksum/version. RootverifiedDarwinarm64archiveSHA256 `f9bb7c0a02506c5d9bf0d1eb1f7ee6c7684f844ae49558308a0427b830e022cc`, filenamego1.26.9.darwin-arm64.tar.gz, size64675343. No insecureTLS/download bypass.
- [ ] Retain the11 actualstdlib findings andx/net0.57.0 findings asRED. UpdateGo toolchain/x-net withminimalrequiredtransitives. Useofficialproxy/sumdb and `go mod tidy -diff`; don't globally disableverification to fetchthem.
- [ ] Updateexactcurrentpins/fixtures, leavingexpectedchecksstrict. Run acceptance/verifierbuild-metadata tests toprove9accepted/olderorwrongbinaryrejected. No blanketrg-replace ofevery1.26.8historicalreference.
- [ ] Run vulnerabilityscansordinary and `-tags vibermate_native_secrets` withthe verifiedGo9binary oncleantrackedsource. Sourcefeatureswerealreadytestedwith8; rerunaffectedHTTP/TLS/proxy/egress/auth/acceptance tests oncewith9 and minimalnewnet. Run releaseNodeprovenance/toolingtests and structural/moduledrift checks. Wholecandidate CIrerunsafterpush, not replacedbyscopedproof.
- [ ] Recordfirsterror, dependencydiff, primarysourcechecksum, exactcommands/Go-version/buildmetadata/scanneroutput/limits. Commitonlyownedpaths, freeze forindependentreview; rootupdatesexistingPR46. Do notpush/merge/publishyourself.

## Task 2: Same-source candidate qualification resumes

Task 1 is complete at `548ff6d0012f7497c88ed6717ada1ef058e7a33d`: verified official Go1.26.9; minimal MVS graph; ordinary/native scans show zero called and zero imported-package findings; 15 affected Go packages pass; Node22 release tooling126PASS/2existing opt-inSKIP; exact provenance/native builds and structural checks pass. Independent spec and quality review approved with no findings. One unimported OpenPGP module advisory remains explicitly recorded. Fresh hosted candidate CI and protected publication remain Task2/release-plan work, not claimed complete here.

- [ ] Review patchscope, stricttoolchainverification andscanresults; resolveactualblockingissues.
- [ ] UpdatePR46 withreviewedSHAandobservefreshCI. Oldfa4ec09scan failures remainfailed evidence; don'trerunoldvulnerablesourceuntilgreen.
- [ ] Continuealreadyapprovedreleasepipeline onlyafterexactcandidatechecks pass, while isolatedUDPworkresumesfromits preservedcheckpoint. No wholeGoalcompletionclaim for thispatchrelease.
