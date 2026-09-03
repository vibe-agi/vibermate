-------------------------- MODULE AccountSelector --------------------------
EXTENDS FiniteSets, Naturals

CONSTANTS Accounts, RouteAccounts, SelectorRevisions, NoAccount

ASSUME /\ Accounts # {}
       /\ RouteAccounts # {}
       /\ RouteAccounts \subseteq Accounts
       /\ SelectorRevisions # {}
       /\ NoAccount \notin Accounts
Modes == {"fixed", "script"}
Phases == {"ready", "selected", "credentialed", "sent", "failed"}

VARIABLES mode,
          phase,
          libraryHead,
          frozenSelector,
          initialFrozenSelector,
          fixedAccount,
          selectedAccount,
          credentialAccount,
          selectorRuns,
          sendCount

vars == <<mode, phase, libraryHead, frozenSelector, initialFrozenSelector,
          fixedAccount, selectedAccount, credentialAccount, selectorRuns,
          sendCount>>

Init ==
    /\ mode \in Modes
    /\ phase = "ready"
    /\ libraryHead \in SelectorRevisions
    /\ frozenSelector = libraryHead
    /\ initialFrozenSelector = frozenSelector
    /\ fixedAccount \in RouteAccounts
    /\ selectedAccount = NoAccount
    /\ credentialAccount = NoAccount
    /\ selectorRuns = 0
    /\ sendCount = 0

PublishNewSelector ==
    /\ libraryHead' \in SelectorRevisions \ {libraryHead}
    /\ UNCHANGED <<mode, phase, frozenSelector, initialFrozenSelector,
                    fixedAccount, selectedAccount, credentialAccount,
                    selectorRuns, sendCount>>

SelectFixed ==
    /\ mode = "fixed"
    /\ phase = "ready"
    /\ selectedAccount' = fixedAccount
    /\ phase' = "selected"
    /\ UNCHANGED <<mode, libraryHead, frozenSelector, initialFrozenSelector,
                    fixedAccount, credentialAccount, selectorRuns, sendCount>>

SelectWithScript ==
    /\ mode = "script"
    /\ phase = "ready"
    /\ selectorRuns = 0
    /\ \E account \in RouteAccounts:
          /\ selectedAccount' = account
          /\ phase' = "selected"
    /\ selectorRuns' = 1
    /\ UNCHANGED <<mode, libraryHead, frozenSelector, initialFrozenSelector,
                    fixedAccount, credentialAccount, sendCount>>

RejectScriptResult ==
    /\ mode = "script"
    /\ phase = "ready"
    /\ selectorRuns = 0
    /\ selectorRuns' = 1
    /\ phase' = "failed"
    /\ UNCHANGED <<mode, libraryHead, frozenSelector, initialFrozenSelector,
                    fixedAccount, selectedAccount, credentialAccount, sendCount>>

AcquireCredential ==
    /\ phase = "selected"
    /\ selectedAccount \in RouteAccounts
    /\ credentialAccount' = selectedAccount
    /\ phase' = "credentialed"
    /\ UNCHANGED <<mode, libraryHead, frozenSelector, initialFrozenSelector,
                    fixedAccount, selectedAccount, selectorRuns, sendCount>>

RejectCredential ==
    /\ phase = "selected"
    /\ phase' = "failed"
    /\ UNCHANGED <<mode, libraryHead, frozenSelector, initialFrozenSelector,
                    fixedAccount, selectedAccount, credentialAccount,
                    selectorRuns, sendCount>>

Send ==
    /\ phase = "credentialed"
    /\ credentialAccount = selectedAccount
    /\ sendCount' = sendCount + 1
    /\ phase' = "sent"
    /\ UNCHANGED <<mode, libraryHead, frozenSelector, initialFrozenSelector,
                    fixedAccount, selectedAccount, credentialAccount,
                    selectorRuns>>

Next == PublishNewSelector \/ SelectFixed \/ SelectWithScript \/
        RejectScriptResult \/ AcquireCredential \/ RejectCredential \/ Send

Spec == Init /\ [][Next]_vars

TypeOK ==
    /\ mode \in Modes
    /\ phase \in Phases
    /\ libraryHead \in SelectorRevisions
    /\ frozenSelector \in SelectorRevisions
    /\ initialFrozenSelector \in SelectorRevisions
    /\ fixedAccount \in RouteAccounts
    /\ selectedAccount \in Accounts \cup {NoAccount}
    /\ credentialAccount \in Accounts \cup {NoAccount}
    /\ selectorRuns \in 0..1
    /\ sendCount \in 0..1

SelectorRevisionIsFrozen == frozenSelector = initialFrozenSelector

SelectionStaysInsideRoute ==
    selectedAccount = NoAccount \/ selectedAccount \in RouteAccounts

CredentialMatchesSelection ==
    credentialAccount = NoAccount \/ credentialAccount = selectedAccount

ScriptRunsAtMostOnce == selectorRuns <= 1

ModeOwnsSelection ==
    /\ (mode = "fixed" /\ selectedAccount # NoAccount)
          => selectedAccount = fixedAccount
    /\ (mode = "script" /\ selectedAccount # NoAccount)
          => selectorRuns = 1

NoSendWithoutSelectedCredential ==
    sendCount = 0 \/
      (credentialAccount = selectedAccount /\ selectedAccount \in RouteAccounts)

FailureCommitsNothing ==
    phase = "failed" => (credentialAccount = NoAccount /\ sendCount = 0)

NoImplicitFallback == sendCount <= 1

=============================================================================
