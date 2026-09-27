# Activate Accounts per Environment Route

Status: accepted

People associate Accounts with an Upstream Service once, then activate one Account inside each Traffic Policy service group. Activation publishes only that Route's manual Account choice; every current and future Capture using the Traffic Policy reads it when its next request begins, while an in-flight request and every other service group remain unchanged. Capture pages report the Account actually used but never own the switch, and JavaScript-selected Routes remain script-owned.
