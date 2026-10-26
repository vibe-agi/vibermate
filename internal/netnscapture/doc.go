// Package netnscapture captures a Linux Agent and every process it starts by
// running it in its own user, network and mount namespace whose only route is
// a TUN device owned by the launcher (ADR 0024). A gVisor netstack turns the
// Agent's packets into flows: TCP goes to the run's flow ingress, DNS is
// answered with fake addresses that map back to names, and other UDP goes
// through vibermated as connect-udp. Attribution is structural, and capture
// fails closed: without the launcher the namespace has no route out.
package netnscapture

// ChildCommand is the hidden vibermate argument that runs RunChild inside the
// new namespaces. It is never typed by a person.
const ChildCommand = "__vibermate-capture-namespace"
