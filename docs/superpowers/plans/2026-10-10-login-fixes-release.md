# Login Fixes Stable Release Plan

> **For agentic workers:** Use superpowers:subagent-driven-development for scoped implementation/review, and verification-before-completion for every delivery claim.

**Goal:** Publish the approved default-port, same-tab Web-session restoration and existing-account OAuth reauthorization fixes as the next independently qualified stable release.

**Architecture:** Reuse the protected signed/notarized distribution and Linux/Homebrew pipeline already used forv0.1.24. Candidate source stays on the stable releaseddbf9745 line, separate from unfinished MCP/global/VLESS WIP. Prepare version metadata, freeze reviewed source, run same-source checks, then publish immutable verified assets.

**Tech Stack:** Existing pinnedGo1.26.8/Flutter3.41.5/Node22.23.1, existingGitHubCI/release tools. No tooling/dependency upgrades.

**Spec:** Approved behavior in `../specs/2026-10-09-server-login-continuity-design.md` and `../specs/2026-10-09-provider-account-reauthorization-proposal.md`; user approved phased delivery and direct publication after tests, while protected signing/notarization approval requirements remain effective.

**Currentcandidate:** featurecommitsf90d972/b29fff8/dc71b24/930f4b1, reviewedmetadata1a8272e andfeedbackcorrection79fb19c. Version0.1.25+27; no newtag/releaseyet. FinalsourceSHAwillincludeapprovedplans andbeexplicitlyrecordedbeforeCI. Originalfullsuitefailuresremainpreserved; scopedpassesarenotcalledfullqualification.

## Global Constraints

- Stable worktree `/Users/null/Code/github/vibe-agi/vibermate/.worktrees/long-session-hotfix`. Product source frozen930f4b1 before version preparation; independent combinedreview underway, no active source writer/test at handoff.
- Latestremote verified2026-10-10: mainprotecteddbf9745b5391723103967889939a5ab4c7fbb7bc, latestReleasev0.1.24. Prepare0.1.25+27, recheck before tag/publication. Never overwritev0.1.24 or claim localbuildpublished.
- Preserve all user data/accounts/trust/runningprocesses and unrelated local work. Onlyownedproductdocs/version paths. One source writer/localtestowner; readonlyreview/hostedCI can run separately.
- Do not weaken approvals, verification, signedartifact/sourcebinding, tests or watchdogs to obtaingreen. Keep original failed fullGo/Flutter logs distinct from successfulisolateddiagnostics.

## Task 1: Candidate version metadata and release notes

**Files:** `ui/flutter_app/pubspec.yaml`; `lib/core/update/product_version.dart` under Flutter; `tool/macos-release/macos-distribution-policy.mjs`; three literal DMG paths in `.github/workflows/macos-developer-id-candidate.yml`; exact version/build fixtures in `prepare-r0-release-evidence.test.mjs`, `macos-distribution-policy.test.mjs`, `macos-installed-candidate-evidence.test.mjs`; new `docs/releases/v0.1.25.md`. Additional exact productversion consumers only if a scoped search demonstrates them. No source feature edits.

**Interface:** Existing build/distribution validators consume these literal productversion/build/filename values. They must all refer to one candidate; unrelated schema/version constants stay untouched.

- [ ] Set product semanticversion0.1.25/build27 and diskImageFilename`ViberMate_0.1.25_universal.dmg`, preserveallotherdistributionpolicy. Never replace unrelated `maximumSchema:"26"` or historicalrelease/evidence documents.
- [ ] Verify the existing version-binding regression detects an intermediate mismatch before completing all metadata updates; retain actual output. Update exact fixture values, not expected test outcomes or integrity checks. Do not add source-text-only change-detector tests.
- [ ] Write concise EN/zh release notes: omittedHTTP(S)defaultports, Websame-tabrefreshvalidsession(no savedpasswords), originalOAuthaccountsigninagain preservingID/settings/links; wrongidentity/cancelnooverwrite. Explicit disabled accounts are not autoenabled. No VLESS/MCP/global/CA newclaim, no live-provider qualificationclaim, no completedpublicationclaim.
- [ ] Run release-tooling Node suite, actionpins/workflowcontract tests, formatting/diffcheck and scopedversionsearch. Record any failures, commands, exactsource and limits. Commit onlyownedpaths, freeze andhand off to rootreview. Do notpush/tag/dispatchrelease yourself.

## Task 2: Freeze and validate candidate source

- [ ] Resolve combinedproductreview and version-metadata review; include onlyapprovedstablechanges. Commit root-ownedapprovedplans/statusdocs separately; preserve unrelateduntrackedVLESSdocs.
- [ ] Push a scoped candidatebranch/PR through existing protectedmain rules. Run appropriate hostedCI and serial localchecks onexactcandidate; sourcechanges invalidate affected evidence. Do notrerun previouslypassedunchangedunit campaignsjustbecausea turnresumed.
- [ ] Observe real jobhandles untilterminal; no duplicatejobs afterobservertimeout. Diagnose actual failures, including prior localtimingfailures if reproduced; do nothide them orincreaseproductdeadlines. Source-boundnormal/build/race/vet/structural/dependency/Flutter/Chrome/packagechecks remain required asapplicable tothisrelease.
- [ ] Merge onlyreviewedqualifiedcandidate bynormalprotectedworkflow; recordfullSHA. Reconfirmnextversionfree beforecreatingtag/release.

## Task 3: Signed/public delivery

- [ ] Dispatch existingprotectedmacOSworkflow frommain with exactmergedcandidateSHA. Respectactualsigning/notarizationenvironmentapprovals; never impersonatehumanapproval. Whilejobswait, continueotherapprovedfeatureworkinitsownworktreeandoriginalhandles.
- [ ] Verify same-sourceunsigned/signed/notarized/installedprovenance andartifact hashes fromthe exactproducer. Preserveoriginalfailures; no trustingjobnames/skippedsteps alone.
- [ ] CreateimmutableGitHubrelease/tag andpublishonlyverifiedassetsets; build/verifyLinuxviaexistingworkflow. UpdateHomebrewthroughisolatedtapworktree, preservinguser'soriginaldirtytapfiles.
- [ ] Independentlydownload/checkpublishedversion/hashes/assets andactualBrewmetadata/installCI. Reportactualdelivery, leavefullproductionGoalactiveforremainingapprovedscope.
