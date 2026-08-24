# ViberMate Runtime Evidence

ViberMate observes AI Agent traffic and preserves the identities needed to reconstruct what happened without inventing relationships from timing, titles, or model names.

## Language

**Capture**:
A bounded observation scope created for one managed client run or one manual capture authority. A managed run keeps the Environment selected at launch.
_Avoid_: Session, conversation

**Environment**:
A named runtime policy selected independently for one Capture. It contains one or more Client Flows together with evidence and tool policies, and never follows from a Client Session.
_Avoid_: Provider, Session, workspace default

**Runtime Server**:
A ViberMate Host reachable at an explicit host and port. Selecting it chooses where Capture control, policy, evidence, and proxy traffic run; it does not select or mutate an Environment, Route, Account, or model.
_Avoid_: Upstream Endpoint, Environment, provider server

**Runtime User**:
A person authorized by a Runtime Server to create Captures and own their usage evidence. A Runtime User is never an upstream authentication Account.
_Avoid_: Account, Provider Account, machine, client

**Client Device**:
One machine used by a Runtime User to connect to a Runtime Server. A Client Device supplies machine and workspace evidence but does not independently grant Capture authority.
_Avoid_: Runtime User, Account, approval

**Login Session**:
A revocable Runtime Server authority issued after a Runtime User authenticates. It may authorize multiple Capture Runs from one Client Device until it expires or is revoked.
_Avoid_: Client Session, Capture, Provider session

**Capture Run**:
A managed Capture created for one launched client process. It freezes the Runtime User, Client Device, Workspace, and Environment authority used by every Exchange it observes.
_Avoid_: Client Session, Conversation, login

**Client Flow**:
One exact client-facing origin and Client Protocol handled by an Environment, together with the Destination Plan for that traffic.
_Avoid_: Endpoint, Route, provider protocol

**Destination Plan**:
The mutually exclusive choice for one Client Flow between preserving its Original Destination and using an explicit Upstream Route.
_Avoid_: Mode, fallback

**Original Destination**:
The exact origin, authentication, and request model selected by the client. Preserving it never bypasses ViberMate interception: observable traffic is still decrypted, parsed, and recorded without replacing those authorities.
_Avoid_: Blind tunnel, direct connection, system transparent

**Upstream Endpoint**:
A user-declared upstream origin and set of accepted protocols. Its name, domain, and model identifiers do not establish a provider identity or authentication method.
_Avoid_: Provider, Account, Route

**Account**:
One authentication authority and transport method belonging to exactly one Upstream Endpoint.
_Avoid_: Client Session, provider, credential string

**Upstream Route**:
An explicit destination through one Upstream Endpoint, one backend protocol, and one Account owned by that Endpoint. A Route may contain exact Model Mappings.
_Avoid_: Endpoint, Client Flow, inferred provider

**Model Mapping**:
One exact requested-model to upstream-model rewrite scoped to an Upstream Route. An unmatched requested model preserves its original opaque identifier.
_Avoid_: Model family, provider alias, fuzzy match

**Client Session**:
The client-native, resumable identity that owns one or more related conversations. Its identifier is opaque and may survive a resume operation.
_Avoid_: Capture, request session

**Conversation**:
One ordered dialogue stream inside a Client Session, belonging either to the main Agent or to one explicitly identified Subagent.
_Avoid_: Session, Exchange

**Session Continuity**:
When an upstream execution target changes, the next model receives the portable prior conversation: instructions, user and Assistant messages, and completed tool calls and results. Provider-private reasoning, caches, and encrypted state remain evidence but are not part of this guarantee.
_Avoid_: Transparent forwarding, exact provider-state continuity

**Session Handoff**:
A new Client Session continues from the portable, recorded context of an earlier Session while preserving an explicit relationship between them.
_Avoid_: Resume, same Session

**Native Resume**:
A client starts a new CaptureRun while continuing an existing Client Session through that client's own resume command.
The new CaptureRun selects its Environment independently; a Client Session never implies or inherits an Environment.
_Avoid_: Hot switch, Session Handoff

**Credential Rotation**:
The secret for one Account changes while the Account continues to represent the same upstream principal and state scope.
_Avoid_: Account Switch

**Account Switch**:
A Route begins using a different Account and therefore a potentially different upstream principal or state scope.
_Avoid_: Credential Rotation, key rotation

**Upstream State Scope**:
The upstream tenancy within which stored responses, files, caches, encrypted state, and other opaque references remain valid.
_Avoid_: Endpoint, provider name

**Turn**:
One user-visible round within a Conversation.
_Avoid_: Conversation, request

**Exchange**:
One captured client request and its terminal downstream outcome, together with routing and evidence. An Exchange is the evidence boundary from which a Turn is presented.
_Avoid_: Conversation, Session

**Usage Observation**:
Protocol-declared model and token counts recorded for one Exchange. Missing values remain unknown; ViberMate does not infer them from model names, payload size, or provider identity.
_Avoid_: Estimate, billing record, model guess

**Actor**:
The client-native Agent or Subagent identity that owns a Conversation.
_Avoid_: Account, model
